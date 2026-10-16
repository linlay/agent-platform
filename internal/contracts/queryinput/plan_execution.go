package queryinput

import "agent-platform/internal/i18n"

// PlanExecutionMessage is the visible input of a Run started by approving a
// plan. It is not persisted: replay renders it from the Run's frozen locale.
func PlanExecutionMessage(locale string) string {
	if i18n.ResolveLocale(i18n.DefaultLocale, locale) == i18n.LocaleZhCN {
		return "执行计划"
	}
	return "Execute planning"
}
