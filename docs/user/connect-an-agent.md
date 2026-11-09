---
{"id":"connect-an-agent","title":"Connect your agent","description":"Give Claude, Codex or another MCP client access to one Book, with read, propose or record access you control.","group":"Start here","order":2,"keywords":["agent","mcp","claude","claude code","claude desktop","codex","connection","oauth","connector","read","propose","write","revoke"]}
---

## How a connection works {#how-connections-work}

Agents use Vult through MCP, the open protocol that Claude, Codex and many other assistants support. Each connection reaches **one Book** at one of three levels:

- **Read** lets the agent look up accounts, entries, balances, sources and reports. It records nothing.
- **Propose** also lets it prepare entries for you to approve in **Approvals**. Nothing reaches your Book until you approve it, and the agent can't post, reverse or change accounts itself.
- **Record** (`write`) also lets it create accounts, record and reverse entries, and add and link sources.

You choose the level when you connect the agent, and you can change it any time in **Settings → Connections**. The change applies to the agent's next request; it keeps its connection and history.

No agent can create Books, grant itself access, close periods, run backups or change your owner settings. Grant even read access only to agents you trust with those records.

## Connect to Vult Cloud {#cloud}

Your agent connects to this address:

```text
https://mcp.vult.ai
```

In **Claude Code**, run:

```sh
claude mcp add --transport http vult https://mcp.vult.ai
```

In **Claude Desktop** or **Claude.ai**, add a custom connector with the same address. Other clients that support remote MCP servers use the same address too.

The first time the agent uses Vult, a browser window opens:

1. Sign in to Vult Cloud.
2. Choose the Book this agent may use, and **Read** or **Write**.
3. Choose **Connect**.

The agent can then work in that Book. Never paste browser cookies or passwords into an agent.

## Connect on your own computer {#on-your-computer}

Create a connection from your own terminal. Use the Book ID from `vult book list`:

```sh
vult connection create --name "Claude" --book BOOK_ID --access write
```

The result includes a ready-made MCP entry. Add it to your client's MCP settings. The command is always `vult mcp` with that connection's ID:

```json
{
  "mcpServers": {
    "vult": {
      "command": "/Library/PrivilegedHelperTools/ai.vult.ledger/vult",
      "args": ["mcp", "--connection", "CONNECTION_ID"]
    }
  }
}
```

For Codex, the same entry in `config.toml` is:

```toml
[mcp_servers.vult]
command = "/Library/PrivilegedHelperTools/ai.vult.ledger/vult"
args = ["mcp", "--connection", "CONNECTION_ID"]
startup_timeout_sec = 90
```

When the agent starts, your computer asks you to approve the session. Approval lasts up to eight hours, or until Vult restarts. Allow time for that prompt: for Claude Code, start it with `MCP_TIMEOUT=90000 claude`.

## See and remove connections {#manage-connections}

In the browser, **Settings → Connections** lists the agents connected to your Books and what each can reach. You can disconnect an agent there.

On your own computer, you can also use:

```sh
vult connection list
vult connection revoke CONNECTION_ID
```

Disconnecting an agent stops its access immediately. What it already recorded stays in the Book, attributed to that agent, and entries it prepared can still be approved.

## Give your agent the bookkeeping skill {#bookkeeping-skill}

The Vult repository includes a [bookkeeping skill](https://github.com/vult-ai/vult/tree/main/skills/vult-bookkeeping) for agents that support skills. It covers choosing accounts and dates, common postings, keeping originals with entries and recovering safely from an interrupted write. A skill helps your agent work well; it doesn't grant any extra access.
