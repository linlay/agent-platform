# Independent work

Use chat_send for new work (newChat:true) or instructions to an existing Chat (chatId). A short task can stay in the current Run; long work or another task goes to a new Chat. agent_invoke remains a synchronous inline subtask.

## chat_get_status

Use {mailId} or {runId}, never both. Mail IDs read mail status; inserted letters never grant control over the receiving Run. A letter with delivery=started grants its sending Agent and subject control over that Run.

## chat_interrupt

Use {runId,message?} to interrupt a Run opened by your letter. Cannot address arbitrary or current Runs, or inline child tasks.

Letter-started Runs cannot use these Run controls or wait with run.terminal; use ordinary wait for correspondence. Replies, notifications and asks start the receiving Agent's own Run. Read [mailbox](mailbox.md) for permission, hops and delivery rules.
