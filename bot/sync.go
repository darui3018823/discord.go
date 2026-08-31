package bot

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"

	dgo "github.com/darui3018823/discord.go"
)

var (
	// ErrDuplicateRemoteCommand is returned when Discord returns two commands
	// with the same type and name, making an incremental plan ambiguous.
	ErrDuplicateRemoteCommand = errors.New("duplicate remote application command")
	// ErrInvalidCommandSync is returned for malformed local or remote inputs.
	ErrInvalidCommandSync = errors.New("invalid command sync input")
)

// CommandSyncAction identifies one incremental synchronization operation.
type CommandSyncAction uint8

const (
	CommandSyncUnchanged CommandSyncAction = iota
	CommandSyncCreate
	CommandSyncUpdate
	CommandSyncDelete
)

func (a CommandSyncAction) String() string {
	switch a {
	case CommandSyncUnchanged:
		return "unchanged"
	case CommandSyncCreate:
		return "create"
	case CommandSyncUpdate:
		return "update"
	case CommandSyncDelete:
		return "delete"
	default:
		return fmt.Sprintf("CommandSyncAction(%d)", a)
	}
}

// CommandSyncChange is one deterministic desired/remote comparison.
type CommandSyncChange struct {
	Action  CommandSyncAction
	Desired *dgo.ApplicationCommand
	Remote  *dgo.ApplicationCommand
}

// CommandSyncReport contains the plan and successfully applied results.
// Applied slices remain empty during a dry run.
type CommandSyncReport struct {
	Changes   []CommandSyncChange
	Created   []*dgo.ApplicationCommand
	Updated   []*dgo.ApplicationCommand
	Deleted   []*dgo.ApplicationCommand
	Unchanged []*dgo.ApplicationCommand
	DryRun    bool
}

// CommandSyncOption configures incremental synchronization.
type CommandSyncOption func(*commandSyncConfig)

type commandSyncConfig struct {
	deleteUnknown bool
	dryRun        bool
}

// WithDeleteUnknownCommands controls deletion of remote commands absent from
// the local router. It defaults to true.
func WithDeleteUnknownCommands(deleteUnknown bool) CommandSyncOption {
	return func(config *commandSyncConfig) { config.deleteUnknown = deleteUnknown }
}

// WithCommandSyncDryRun returns a plan without create, edit, or delete calls.
func WithCommandSyncDryRun(dryRun bool) CommandSyncOption {
	return func(config *commandSyncConfig) { config.dryRun = dryRun }
}

// PlanCommandSync calculates a deterministic semantic command diff. Discord
// IDs, versions, scope IDs, omitted defaults, and nil/empty collection forms
// do not cause updates.
func PlanCommandSync(desired, remote []*dgo.ApplicationCommand, deleteUnknown bool) ([]CommandSyncChange, error) {
	desiredByKey, err := indexSyncCommands(desired, false)
	if err != nil {
		return nil, err
	}
	remoteByKey, err := indexSyncCommands(remote, true)
	if err != nil {
		return nil, err
	}
	keys := make([]commandKey, 0, len(desiredByKey)+len(remoteByKey))
	seen := make(map[commandKey]struct{}, len(desiredByKey)+len(remoteByKey))
	for key := range desiredByKey {
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	for key := range remoteByKey {
		if _, exists := seen[key]; !exists {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].typeID != keys[j].typeID {
			return keys[i].typeID < keys[j].typeID
		}
		return keys[i].name < keys[j].name
	})

	changes := make([]CommandSyncChange, 0, len(keys))
	for _, key := range keys {
		local := desiredByKey[key]
		existing := remoteByKey[key]
		switch {
		case local == nil && !deleteUnknown:
			continue
		case local == nil:
			changes = append(changes, CommandSyncChange{Action: CommandSyncDelete, Remote: cloneSyncCommand(existing)})
		case existing == nil:
			changes = append(changes, CommandSyncChange{Action: CommandSyncCreate, Desired: cloneSyncCommand(local)})
		case commandsSemanticallyEqual(local, existing):
			changes = append(changes, CommandSyncChange{Action: CommandSyncUnchanged, Desired: cloneSyncCommand(local), Remote: cloneSyncCommand(existing)})
		default:
			changes = append(changes, CommandSyncChange{Action: CommandSyncUpdate, Desired: cloneSyncCommand(local), Remote: cloneSyncCommand(existing)})
		}
	}
	return changes, nil
}

// SyncCommandDiff fetches remote commands, calculates a semantic diff, and
// applies only required create, edit, and delete operations. An empty guildID
// targets global commands.
func (b *Bot) SyncCommandDiff(ctx context.Context, applicationID, guildID string, options ...CommandSyncOption) (*CommandSyncReport, error) {
	if ctx == nil {
		return nil, errors.New("context must not be nil")
	}
	if applicationID == "" {
		return nil, errors.New("application ID must not be empty")
	}
	config := commandSyncConfig{deleteUnknown: true}
	for _, option := range options {
		if option != nil {
			option(&config)
		}
	}
	remote, err := b.session.ApplicationCommands(applicationID, guildID, dgo.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("list application commands: %w", err)
	}
	changes, err := PlanCommandSync(b.CommandDefinitions(), remote, config.deleteUnknown)
	if err != nil {
		return nil, err
	}
	report := &CommandSyncReport{Changes: changes, DryRun: config.dryRun}
	if config.dryRun {
		return report, nil
	}
	for _, change := range changes {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		switch change.Action {
		case CommandSyncCreate:
			created, createErr := b.session.ApplicationCommandCreate(applicationID, guildID, change.Desired, dgo.WithContext(ctx))
			if createErr != nil {
				return report, fmt.Errorf("create application command %q: %w", change.Desired.Name, createErr)
			}
			report.Created = append(report.Created, created)
		case CommandSyncUpdate:
			if change.Remote.ID == "" {
				return report, fmt.Errorf("%w: remote command %q has no ID", ErrInvalidCommandSync, change.Remote.Name)
			}
			updated, updateErr := b.session.ApplicationCommandEdit(applicationID, guildID, change.Remote.ID, change.Desired, dgo.WithContext(ctx))
			if updateErr != nil {
				return report, fmt.Errorf("update application command %q: %w", change.Desired.Name, updateErr)
			}
			report.Updated = append(report.Updated, updated)
		case CommandSyncDelete:
			if change.Remote.ID == "" {
				return report, fmt.Errorf("%w: remote command %q has no ID", ErrInvalidCommandSync, change.Remote.Name)
			}
			if deleteErr := b.session.ApplicationCommandDelete(applicationID, guildID, change.Remote.ID, dgo.WithContext(ctx)); deleteErr != nil {
				return report, fmt.Errorf("delete application command %q: %w", change.Remote.Name, deleteErr)
			}
			report.Deleted = append(report.Deleted, change.Remote)
		case CommandSyncUnchanged:
			report.Unchanged = append(report.Unchanged, change.Remote)
		}
	}
	return report, nil
}

func indexSyncCommands(commands []*dgo.ApplicationCommand, remote bool) (map[commandKey]*dgo.ApplicationCommand, error) {
	indexed := make(map[commandKey]*dgo.ApplicationCommand, len(commands))
	for _, command := range commands {
		if command == nil || command.Name == "" {
			return nil, ErrInvalidCommandSync
		}
		key := keyFor(command)
		if _, exists := indexed[key]; exists {
			if remote {
				return nil, fmt.Errorf("%w: %s", ErrDuplicateRemoteCommand, command.Name)
			}
			return nil, fmt.Errorf("%w: duplicate local command %s", ErrInvalidCommandSync, command.Name)
		}
		indexed[key] = command
	}
	return indexed, nil
}

func commandsSemanticallyEqual(desired, remote *dgo.ApplicationCommand) bool {
	left := cloneSyncCommand(desired)
	right := cloneSyncCommand(remote)
	normalizeSyncCommand(left, left)
	normalizeSyncCommand(right, left)
	return reflect.DeepEqual(left, right)
}

func normalizeSyncCommand(command, desired *dgo.ApplicationCommand) {
	command.ID = ""
	command.ApplicationID = ""
	command.GuildID = ""
	command.Version = ""
	if command.Type == 0 {
		command.Type = dgo.ChatApplicationCommand
	}
	if desired.NameLocalizations == nil {
		command.NameLocalizations = nil
	} else if len(*desired.NameLocalizations) == 0 {
		command.NameLocalizations = nil
	}
	if desired.DescriptionLocalizations == nil {
		command.DescriptionLocalizations = nil
	} else if len(*desired.DescriptionLocalizations) == 0 {
		command.DescriptionLocalizations = nil
	}
	if desired.DefaultPermission == nil {
		command.DefaultPermission = nil
	}
	if desired.DefaultMemberPermissions == nil {
		command.DefaultMemberPermissions = nil
	}
	if desired.NSFW == nil {
		command.NSFW = nil
	}
	if desired.DMPermission == nil {
		command.DMPermission = nil
	}
	if desired.Contexts == nil {
		command.Contexts = nil
	}
	if desired.IntegrationTypes == nil {
		command.IntegrationTypes = nil
	}
	if desired.Handler == nil {
		command.Handler = nil
	}
	if len(command.Options) == 0 {
		command.Options = nil
	}
	normalizeSyncOptions(command.Options)
}

func normalizeSyncOptions(options []*dgo.ApplicationCommandOption) {
	for _, option := range options {
		if option == nil {
			continue
		}
		if len(option.NameLocalizations) == 0 {
			option.NameLocalizations = nil
		}
		if len(option.DescriptionLocalizations) == 0 {
			option.DescriptionLocalizations = nil
		}
		if len(option.ChannelTypes) == 0 {
			option.ChannelTypes = nil
		}
		if len(option.Options) == 0 {
			option.Options = nil
		}
		if len(option.Choices) == 0 {
			option.Choices = nil
		}
		for _, choice := range option.Choices {
			if choice != nil && len(choice.NameLocalizations) == 0 {
				choice.NameLocalizations = nil
			}
		}
		normalizeSyncOptions(option.Options)
	}
}

func cloneSyncCommand(command *dgo.ApplicationCommand) *dgo.ApplicationCommand {
	if command == nil {
		return nil
	}
	cloned := *command
	cloned.NameLocalizations = cloneLocaleMapPointer(command.NameLocalizations)
	cloned.DescriptionLocalizations = cloneLocaleMapPointer(command.DescriptionLocalizations)
	cloned.Options = cloneSyncOptions(command.Options)
	if command.Contexts != nil {
		contexts := append([]dgo.InteractionContextType(nil), (*command.Contexts)...)
		cloned.Contexts = &contexts
	}
	if command.IntegrationTypes != nil {
		integrationTypes := append([]dgo.ApplicationIntegrationType(nil), (*command.IntegrationTypes)...)
		cloned.IntegrationTypes = &integrationTypes
	}
	return &cloned
}

func cloneSyncOptions(options []*dgo.ApplicationCommandOption) []*dgo.ApplicationCommandOption {
	if options == nil {
		return nil
	}
	cloned := make([]*dgo.ApplicationCommandOption, len(options))
	for index, option := range options {
		if option == nil {
			continue
		}
		copyOption := *option
		copyOption.NameLocalizations = cloneLocaleMap(option.NameLocalizations)
		copyOption.DescriptionLocalizations = cloneLocaleMap(option.DescriptionLocalizations)
		copyOption.ChannelTypes = append([]dgo.ChannelType(nil), option.ChannelTypes...)
		copyOption.Options = cloneSyncOptions(option.Options)
		if option.Choices != nil {
			copyOption.Choices = make([]*dgo.ApplicationCommandOptionChoice, len(option.Choices))
			for choiceIndex, choice := range option.Choices {
				if choice == nil {
					continue
				}
				copyChoice := *choice
				copyChoice.NameLocalizations = cloneLocaleMap(choice.NameLocalizations)
				copyOption.Choices[choiceIndex] = &copyChoice
			}
		}
		cloned[index] = &copyOption
	}
	return cloned
}

func cloneLocaleMapPointer(localizations *map[dgo.Locale]string) *map[dgo.Locale]string {
	if localizations == nil {
		return nil
	}
	cloned := cloneLocaleMap(*localizations)
	return &cloned
}

func cloneLocaleMap(localizations map[dgo.Locale]string) map[dgo.Locale]string {
	if localizations == nil {
		return nil
	}
	cloned := make(map[dgo.Locale]string, len(localizations))
	for locale, value := range localizations {
		cloned[locale] = value
	}
	return cloned
}
