# Implementation Contract

This repository's work items live in `.devtop/tickets/`. Each ticket is a
markdown file with YAML frontmatter. Your job is to implement open tickets,
one at a time, in order.

This file is also the workflow documentation. Read it fully before you act.

## Entry

A human starts you with one sentence:

> Read `.devtop/AGENTS.md` and implement the open tickets in order.

That sentence is the whole workflow. You read this file, then you follow the
loop below. Do not ask for more setup.

## The loop

1. List `.devtop/tickets/*.md`. Pick the open tickets (frontmatter
   `status: open`), oldest ticket id first, or in the listed order when
   dependencies suggest an order.
2. For the current ticket:

   - Mark it claimed. Set `claimed_by` in the frontmatter to your agent id.
   - Read its acceptance criteria.
   - Implement the ticket in the codebase.
   - Write or extend the test file the ticket names (for example
     `test/playfield.test.js`). Write the test first: it must fail before
     your change and pass after.
   - Run the focused tests, then the full test suite. Both must pass.
   - Set `status: done` and add a comment. The comment records evidence:
     the files changed and the test command with its result.
   - Commit this ticket alone. Use a message shaped
     `ticket: <id> — <title>`.

3. Repeat for the next open ticket.

## When a ticket cannot be completed

Do not mark it done. Set `status: in-progress`, add a comment that names the
blocker, and continue with the next ticket. When the loop ends, summarize the
blocked tickets.

## Rules

- One commit per ticket. Never bundle tickets together.
- Acceptance criteria are the contract. "Done" means the criteria pass and
  the declared tests pass.
- A criterion marked `manual` cannot be checked by a test. Verify it by
  observation, note that in the comment, and only then mark done.
- Do not touch files that belong to unrelated tickets or concerns.
- After each completed ticket, report in one short message: the ticket id,
  the test result, and the commit.
- After five tickets in one session, stop and ask before continuing.