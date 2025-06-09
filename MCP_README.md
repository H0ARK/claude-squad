# Claude Squad MCP Server

This document explains how to use Claude Squad as a Model Context Protocol (MCP) server that Claude can connect to for agent management.

## What is MCP?

Model Context Protocol (MCP) is a standardized protocol that allows AI assistants like Claude to connect to external tools and services. Claude Squad implements an MCP server that provides agent management capabilities through tmux sessions.

## Features

The Claude Squad MCP server provides the following tools:

### 1. `launch_agent`
Launch a new agent in a tmux session.

**Parameters:**
- `task` (required): Description of the task for the new agent
- `program` (optional): Program to run (default: "aider")

**Example:**
```
Launch an agent to fix the login bug in the authentication module
```

### 2. `list_agents`
List all active agents and their status.

**Parameters:** None

### 3. `send_message`
Send a message to a specific agent via tmux.

**Parameters:**
- `agent_id` (required): ID of the target agent
- `message` (required): Message to send to the agent

### 4. `get_agent_output`
Get recent output from an agent's tmux session.

**Parameters:**
- `agent_id` (required): ID of the agent to get output from
- `lines` (optional): Number of lines to retrieve (default: 50)

## Setup Instructions

### 1. Build Claude Squad

```bash
go build -o claude-squad .
```

### 2. Configure Claude Desktop

Create or edit the Claude Desktop configuration file:

**macOS:** `~/Library/Application Support/Claude/claude_desktop_config.json`
**Windows:** `%APPDATA%\Claude\claude_desktop_config.json`
**Linux:** `~/.config/Claude/claude_desktop_config.json`

Add the following configuration:

```json
{
  "mcpServers": {
    "claude-squad": {
      "command": "/absolute/path/to/your/claude-squad",
      "args": []
    }
  }
}
```

**Important:** Replace `/absolute/path/to/your/claude-squad` with the actual absolute path to your compiled binary.

### 3. Restart Claude Desktop

Completely quit and restart Claude Desktop for the configuration to take effect.

## Usage Examples

Once configured, you can use Claude Squad through Claude Desktop:

### Launch a New Agent
```
Can you launch an agent to refactor the user authentication code?
```

### Check Agent Status
```
List all active agents and their current status
```

### Send Commands to an Agent
```
Send the message "add unit tests for the login function" to agent-1234567890
```

### Get Agent Output
```
Show me the last 20 lines of output from agent-1234567890
```

## How It Works

1. **Agent Management**: Each agent runs in its own tmux session, allowing for isolated environments
2. **Communication**: Messages are sent to agents via tmux's `send-keys` command
3. **Output Capture**: Agent output is captured using tmux's `capture-pane` command
4. **Session Tracking**: The server maintains a registry of active agents and their tmux sessions

## Architecture

```
Claude Desktop → MCP Protocol → Claude Squad Server → tmux Sessions → Agents
```

- **Claude Desktop**: The client that sends requests
- **MCP Protocol**: JSON-RPC 2.0 over stdio
- **Claude Squad Server**: Manages agents and tmux sessions
- **tmux Sessions**: Isolated environments for each agent
- **Agents**: Individual programs (aider, cursor, etc.) running tasks

## Troubleshooting

### Server Not Connecting
1. Check that the path in `claude_desktop_config.json` is correct and absolute
2. Ensure the binary has execute permissions: `chmod +x claude-squad`
3. Check Claude Desktop logs for error messages

### Agents Not Launching
1. Ensure tmux is installed: `tmux -V`
2. Check that the specified program (e.g., aider) is available in PATH
3. Verify you're in a git repository (required by claude-squad)

### Testing the Server
You can test the MCP server manually:

```bash
echo '{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": "2024-11-05", "capabilities": {}, "clientInfo": {"name": "test-client", "version": "1.0.0"}}}' | ./claude-squad
```

This should return a JSON response indicating the server is working.

## Requirements

- Go 1.19+ (for building)
- tmux (for session management)
- Git repository (claude-squad requirement)
- Claude Desktop (for MCP client)

## Security Notes

- Agents run with the same permissions as the user running claude-squad
- tmux sessions are created with default security settings
- All communication happens locally via stdio (no network exposure)
