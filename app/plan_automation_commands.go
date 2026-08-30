package app

import (
	"fmt"
	"strings"

	"github.com/snowmerak/q/config"
)

type planAutomationCommandTarget uint8

const (
	planAutomationAutoApprove planAutomationCommandTarget = iota
	planAutomationAutoResolve
	planAutomationAutonomous
)

type planAutomationCommandAction uint8

const (
	planAutomationStatus planAutomationCommandAction = iota
	planAutomationEnable
	planAutomationDisable
)

type planAutomationCommand struct {
	target planAutomationCommandTarget
	action planAutomationCommandAction
	valid  bool
}

func parsePlanAutomationCommand(value string) (planAutomationCommand, bool) {
	fields := strings.Fields(strings.TrimSpace(value))
	if len(fields) == 0 {
		return planAutomationCommand{}, false
	}
	var target planAutomationCommandTarget
	switch fields[0] {
	case "/auto-approve":
		target = planAutomationAutoApprove
	case "/auto-resolve":
		target = planAutomationAutoResolve
	case "/autonomous":
		target = planAutomationAutonomous
	default:
		return planAutomationCommand{}, false
	}
	command := planAutomationCommand{target: target}
	if len(fields) == 1 {
		command.action = planAutomationStatus
		command.valid = true
		return command, true
	}
	if len(fields) != 2 {
		return command, true
	}
	switch fields[1] {
	case "status":
		command.action = planAutomationStatus
	case "on":
		command.action = planAutomationEnable
	case "off":
		command.action = planAutomationDisable
	default:
		return command, true
	}
	command.valid = true
	return command, true
}

func (c planAutomationCommand) usage() string {
	return fmt.Sprintf("Usage: %s [on|off|status]", c.name())
}

func (c planAutomationCommand) name() string {
	switch c.target {
	case planAutomationAutoApprove:
		return "/auto-approve"
	case planAutomationAutoResolve:
		return "/auto-resolve"
	default:
		return "/autonomous"
	}
}

func (c planAutomationCommand) apply(value config.PlanConfig) (config.PlanConfig, bool) {
	if !c.valid || c.action == planAutomationStatus {
		return value, false
	}
	enabled := c.action == planAutomationEnable
	switch c.target {
	case planAutomationAutoApprove:
		value.AutoApprove = enabled
	case planAutomationAutoResolve:
		value.AutoResolve = enabled
	case planAutomationAutonomous:
		value.AutoApprove = enabled
		value.AutoResolve = enabled
	}
	return value, true
}

func renderPlanAutomationCommand(
	command planAutomationCommand,
	configured config.PlanConfig,
	effective config.PlanConfig,
	saved bool,
) string {
	if command.target == planAutomationAutonomous {
		configuredState := formatPlanAutomationPair(configured)
		if configured != effective {
			prefix := "Plan automation"
			if saved {
				prefix = "Plan automation saved"
			}
			return fmt.Sprintf("%s: configured %s · effective %s for this process.", prefix,
				configuredState, formatPlanAutomationPair(effective))
		}
		if saved {
			return "Plan automation saved: " + configuredState + "."
		}
		return "Plan automation: " + configuredState + "."
	}
	label := "Auto-approve"
	configuredValue, effectiveValue := configured.AutoApprove, effective.AutoApprove
	if command.target == planAutomationAutoResolve {
		label = "Auto-resolve"
		configuredValue, effectiveValue = configured.AutoResolve, effective.AutoResolve
	}
	if configuredValue != effectiveValue {
		verb := ""
		if saved {
			verb = " saved"
		}
		return fmt.Sprintf("%s%s: configured %s · effective %s for this process.",
			label, verb, formatPlanAutomationBool(configuredValue), formatPlanAutomationBool(effectiveValue))
	}
	if saved {
		return fmt.Sprintf("%s saved %s.", label, formatPlanAutomationBool(configuredValue))
	}
	return fmt.Sprintf("%s is %s.", label, formatPlanAutomationBool(configuredValue))
}

func formatPlanAutomationPair(value config.PlanConfig) string {
	return fmt.Sprintf("auto-approve %s · auto-resolve %s",
		formatPlanAutomationBool(value.AutoApprove), formatPlanAutomationBool(value.AutoResolve))
}

func formatPlanAutomationBool(value bool) string {
	if value {
		return "on"
	}
	return "off"
}

func (m *model) runPlanAutomationCommand(command planAutomationCommand) string {
	if !command.valid {
		return command.usage()
	}
	configured := m.config.Plan
	next, save := command.apply(configured)
	if save {
		value := m.config
		value.Plan = next
		if err := m.store.Save(value); err != nil {
			return err.Error()
		}
		m.config = value
		configured = next
	}
	return renderPlanAutomationCommand(command, configured, configured, save)
}
