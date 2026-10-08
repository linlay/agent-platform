---
name: kanban-control
description: Manage Desktop kanban issues, including listing, inspecting, creating, updating, deleting and moving issues.
---

# Kanban Control

Requires builtin.kanban-control mounted and a connected Desktop host. Use desktop_kanban with {action,args}. The tool definition only names the capability; read [kanban](references/kanban.md) for every action's arguments, types, constraints and examples before calling it.

Desktop owns execution, results and workflow progression. Do not launch duplicate Chat runs or manually move issues to simulate completion. Preserve Desktop approval and revision checks. Never bypass rejected operations through shell or file tools.

This connector grants only kanban operations. Chat and Automation tools require builtin.task-control; platform administration requires builtin.platform-control; pages require builtin.web-control.
