package cli

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"text/tabwriter"
	"unicode"

	"github.com/ReanSn0w/horizon/internal/config"
	"github.com/ReanSn0w/horizon/internal/instructions"
	flags "github.com/umputun/go-flags"
)

type skillsCommand struct{}
type skillsListCommand struct {
	app    *App
	global *globalOptions
}
type skillsValidateCommand struct {
	app    *App
	global *globalOptions
	ID     string `long:"id" required:"true" description:"skill directory ID (see skills list)"`
}
type skillsStateCommand struct {
	app     *App
	global  *globalOptions
	disable bool
	ID      string `long:"id" required:"true" description:"skill directory ID (see skills list)"`
}

func validateSkillID(id string) error {
	if !instructions.ValidSkillID(id) {
		return usage(fmt.Sprintf("invalid skill ID %q; use a directory ID from skills list", id), nil)
	}
	return nil
}
func (c *skillsValidateCommand) ValidateArgs() error { return validateSkillID(c.ID) }
func (c *skillsStateCommand) ValidateArgs() error    { return validateSkillID(c.ID) }

func (a *App) addSkillCommands(parser *flags.Parser, global *globalOptions) error {
	group, err := parser.AddCommand("skills", "Manage skills", "List, validate, enable or disable local skills without contacting a provider.", &skillsCommand{})
	if err != nil {
		return err
	}
	for _, entry := range []struct {
		name, description string
		command           any
	}{
		{"list", "List skill IDs, names, descriptions and active flags.", &skillsListCommand{app: a, global: global}},
		{"validate", "Validate a skill's SKILL.md on disk.", &skillsValidateCommand{app: a, global: global}},
		{"enable", "Remove a skill ID from disabled_skills for the next turn.", &skillsStateCommand{app: a, global: global}},
		{"disable", "Add a skill ID to disabled_skills for the next turn.", &skillsStateCommand{app: a, global: global, disable: true}},
	} {
		if _, err := group.AddCommand(entry.name, entry.description, entry.description, entry.command); err != nil {
			return err
		}
	}
	return nil
}

func skillConfigError(err error) error {
	var commandError *Error
	if errors.As(err, &commandError) {
		return err
	}
	var pathError *os.PathError
	if errors.As(err, &pathError) {
		return failure(err.Error(), err)
	}
	return usage(err.Error(), err)
}

func tableCell(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	if value == "" {
		return "—"
	}
	return value
}

func (c *skillsListCommand) Execute(args []string) error {
	if err := rejectArgs(args); err != nil {
		return err
	}
	disabled, err := config.LoadDisabledSkills(c.global.Home)
	if err != nil {
		return skillConfigError(err)
	}
	entries, err := instructions.ListSkills(c.global.Home)
	if err != nil {
		return failure(err.Error(), err)
	}
	writer := tabwriter.NewWriter(c.app.out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "ID\tNAME\tDESCRIPTION\tACTIVE")
	for _, entry := range entries {
		fmt.Fprintf(writer, "%s\t%s\t%s\t%t\n", tableCell(entry.ID), tableCell(entry.Skill.Name), tableCell(entry.Skill.Description), !slices.Contains(disabled, entry.ID))
		if entry.Err != nil {
			fmt.Fprintf(c.app.errOut, "horizon: %v\n", entry.Err)
		}
	}
	if err := writer.Flush(); err != nil {
		return failure(err.Error(), err)
	}
	return nil
}

func (c *skillsValidateCommand) Execute(args []string) error {
	if err := rejectArgs(args); err != nil {
		return err
	}
	if _, err := instructions.InspectSkill(c.global.Home, c.ID); err != nil {
		return failure(err.Error(), err)
	}
	fmt.Fprintf(c.app.out, "Skill %q: valid SKILL.md.\n", c.ID)
	return nil
}

func (c *skillsStateCommand) Execute(args []string) error {
	if err := rejectArgs(args); err != nil {
		return err
	}
	if _, err := instructions.SkillDirectory(c.global.Home, c.ID); err != nil {
		return failure(err.Error(), err)
	}
	// The callback uses the current config while its write lock is held.
	check := func(disabled []string) error {
		selected, err := instructions.InspectSkill(c.global.Home, c.ID)
		if err != nil {
			return failure(err.Error(), err)
		}
		entries, err := instructions.ListSkills(c.global.Home)
		if err != nil {
			return failure(err.Error(), err)
		}
		for _, entry := range entries {
			if entry.ID != c.ID && !slices.Contains(disabled, entry.ID) && entry.Skill.Name == selected.Name {
				return failure(fmt.Sprintf("ambiguous skill name %q: IDs %q and %q", selected.Name, c.ID, entry.ID), nil)
			}
		}
		return nil
	}
	changed, err := config.UpdateDisabledSkills(c.global.Home, c.ID, c.disable, check)
	if err != nil {
		return skillConfigError(err)
	}
	state := "enabled"
	if c.disable {
		state = "disabled"
	}
	if changed {
		fmt.Fprintf(c.app.out, "Skill %q: %s; applies from the next turn.\n", c.ID, state)
	} else {
		fmt.Fprintf(c.app.out, "Skill %q: already %s.\n", c.ID, state)
	}
	return nil
}
