#!/bin/bash

# Test script for the MCP server
echo "Testing claude-squad MCP server..."

# Build the server
echo "Building server..."
go build -o claude-squad .

# Test the MCP server with a simple initialize request
echo "Testing initialize request..."
echo '{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": "2024-11-05", "capabilities": {}, "clientInfo": {"name": "test-client", "version": "1.0.0"}}}' | ./claude-squad

echo ""
echo "If you see a JSON response above, the MCP server is working!"
echo ""
echo "To use with Claude Desktop:"
echo "1. Copy claude_desktop_config.json to ~/Library/Application Support/Claude/claude_desktop_config.json (on macOS)"
echo "2. Update the 'command' path to point to your claude-squad binary"
echo "3. Restart Claude Desktop"
