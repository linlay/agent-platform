---
name: kanban-control
description: Manage Desktop kanban issues, including listing, inspecting, creating, updating, deleting and moving issues.
---

# Kanban Control

Requires builtin.kanban-control mounted and a connected Desktop host. Use desktop_kanban with {action,args}. Read [kanban](references/kanban.md) before operating on issues.

Desktop owns execution, results and workflow progression. Do not launch duplicate Chat runs or manually move issues to simulate completion. Preserve Desktop approval and revision checks. Never bypass rejected operations through shell or file tools.

This connector grants only kanban operations. Chat and Automation tools require builtin.task-control; platform administration requires builtin.platform-control; pages require builtin.web-control.
