package mcp

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// JSON-RPC 2.0 base types
type JSONRPCRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id,omitempty"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

type JSONRPCResponse struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      interface{}   `json:"id,omitempty"`
	Result  interface{}   `json:"result,omitempty"`
	Error   *JSONRPCError `json:"error,omitempty"`
}

type JSONRPCError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// JSON-RPC error codes
const (
	ParseError     = -32700
	InvalidRequest = -32600
	MethodNotFound = -32601
	InvalidParams  = -32602
	InternalError  = -32603
)

// MCP specific types
type InitializeParams struct {
	ProtocolVersion string                 `json:"protocolVersion"`
	Capabilities    map[string]interface{} `json:"capabilities"`
	ClientInfo      ClientInfo             `json:"clientInfo"`
}

type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type InitializeResult struct {
	ProtocolVersion string       `json:"protocolVersion"`
	ServerInfo      ServerInfo   `json:"serverInfo"`
	Capabilities    Capabilities `json:"capabilities"`
}

type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type Capabilities struct {
	Tools     map[string]interface{} `json:"tools,omitempty"`
	Resources map[string]interface{} `json:"resources,omitempty"`
	Prompts   map[string]interface{} `json:"prompts,omitempty"`
}

type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type ListToolsResult struct {
	Tools []Tool `json:"tools"`
}

type CallToolResult struct {
	Content []ToolContent `json:"content"`
}

type ToolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type ListResourcesResult struct {
	Resources []interface{} `json:"resources"`
}

type ListPromptsResult struct {
	Prompts []interface{} `json:"prompts"`
}

// Agent management types
type AgentInfo struct {
	ID         string    `json:"id"`
	SessionID  string    `json:"session_id"`
	Task       string    `json:"task,omitempty"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
	LastActive time.Time `json:"last_active"`
}

type MCPServer struct {
	agents  map[string]AgentInfo
	mu      sync.RWMutex
	encoder *json.Encoder
	decoder *json.Decoder
}

func NewMCPServer() *MCPServer {
	return &MCPServer{
		agents:  make(map[string]AgentInfo),
		encoder: json.NewEncoder(os.Stdout),
		decoder: json.NewDecoder(os.Stdin),
	}
}

// Helper functions for JSON-RPC responses
func (s *MCPServer) sendResponse(response interface{}) error {
	return s.encoder.Encode(response)
}

func (s *MCPServer) sendError(id interface{}, code int, message string) error {
	response := JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error: &JSONRPCError{
			Code:    code,
			Message: message,
		},
	}
	return s.sendResponse(response)
}

// Core MCP protocol methods
func (s *MCPServer) handleInitialize(id interface{}, params interface{}) error {
	log.Printf("Handling initialize request")

	response := JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result: InitializeResult{
			ProtocolVersion: "2024-11-05",
			ServerInfo: ServerInfo{
				Name:    "claude-squad-mcp",
				Version: "1.0.0",
			},
			Capabilities: Capabilities{
				Tools: map[string]interface{}{},
			},
		},
	}

	return s.sendResponse(response)
}

func (s *MCPServer) handleToolsList(id interface{}) error {
	log.Printf("Handling tools/list request")

	// Define tool schemas
	launchAgentSchema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"task": {
				"type": "string",
				"description": "Description of the task for the new agent"
			},
			"program": {
				"type": "string",
				"description": "Program to run (e.g., 'aider', 'cursor')",
				"default": "aider"
			}
		},
		"required": ["task"]
	}`)

	listAgentsSchema := json.RawMessage(`{
		"type": "object",
		"properties": {},
		"additionalProperties": false
	}`)

	sendMessageSchema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"agent_id": {
				"type": "string",
				"description": "ID of the target agent"
			},
			"message": {
				"type": "string",
				"description": "Message to send to the agent"
			}
		},
		"required": ["agent_id", "message"]
	}`)

	getOutputSchema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"agent_id": {
				"type": "string",
				"description": "ID of the agent to get output from"
			},
			"lines": {
				"type": "number",
				"description": "Number of lines to retrieve",
				"default": 50
			}
		},
		"required": ["agent_id"]
	}`)

	tools := []Tool{
		{
			Name:        "launch_agent",
			Description: "Launch a new agent in a tmux session",
			InputSchema: launchAgentSchema,
		},
		{
			Name:        "list_agents",
			Description: "List all active agents and their status",
			InputSchema: listAgentsSchema,
		},
		{
			Name:        "send_message",
			Description: "Send a message to a specific agent via tmux",
			InputSchema: sendMessageSchema,
		},
		{
			Name:        "get_agent_output",
			Description: "Get recent output from an agent's tmux session",
			InputSchema: getOutputSchema,
		},
	}

	response := JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result: ListToolsResult{
			Tools: tools,
		},
	}

	return s.sendResponse(response)
}

func (s *MCPServer) handleResourcesList(id interface{}) error {
	log.Printf("Handling resources/list request")

	response := JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result: ListResourcesResult{
			Resources: []interface{}{},
		},
	}

	return s.sendResponse(response)
}

func (s *MCPServer) handlePromptsList(id interface{}) error {
	log.Printf("Handling prompts/list request")

	response := JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result: ListPromptsResult{
			Prompts: []interface{}{},
		},
	}

	return s.sendResponse(response)
}

func (s *MCPServer) handleToolsCall(id interface{}, params interface{}) error {
	log.Printf("Handling tools/call request")

	paramsMap, ok := params.(map[string]interface{})
	if !ok {
		return s.sendError(id, InvalidParams, "Invalid parameters")
	}

	toolName, ok := paramsMap["name"].(string)
	if !ok {
		return s.sendError(id, InvalidParams, "Tool name is required")
	}

	args, ok := paramsMap["arguments"].(map[string]interface{})
	if !ok {
		args = make(map[string]interface{})
	}

	log.Printf("Calling tool: %s with args: %v", toolName, args)

	var result string
	var err error

	switch toolName {
	case "launch_agent":
		result, err = s.launchAgent(args)
	case "list_agents":
		result, err = s.listAgents(args)
	case "send_message":
		result, err = s.sendMessage(args)
	case "get_agent_output":
		result, err = s.getAgentOutput(args)
	default:
		return s.sendError(id, MethodNotFound, fmt.Sprintf("Unknown tool: %s", toolName))
	}

	if err != nil {
		return s.sendError(id, InternalError, err.Error())
	}

	response := JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result: CallToolResult{
			Content: []ToolContent{
				{
					Type: "text",
					Text: result,
				},
			},
		},
	}

	return s.sendResponse(response)
}

// Agent management methods
func (s *MCPServer) launchAgent(args map[string]interface{}) (string, error) {
	task, ok := args["task"].(string)
	if !ok || task == "" {
		return "", fmt.Errorf("task is required")
	}

	program, ok := args["program"].(string)
	if !ok {
		program = "aider"
	}

	// Generate unique agent ID
	agentID := fmt.Sprintf("agent-%d", time.Now().Unix())
	sessionID := fmt.Sprintf("claude-squad-%s", agentID)

	// Create tmux session
	cmd := exec.Command("tmux", "new-session", "-d", "-s", sessionID, program)
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("failed to create tmux session: %w", err)
	}

	// Store agent info
	s.mu.Lock()
	s.agents[agentID] = AgentInfo{
		ID:         agentID,
		SessionID:  sessionID,
		Task:       task,
		Status:     "active",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
	}
	s.mu.Unlock()

	return fmt.Sprintf("Agent %s launched successfully in tmux session %s with task: %s", agentID, sessionID, task), nil
}

func (s *MCPServer) listAgents(args map[string]interface{}) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.agents) == 0 {
		return "No active agents", nil
	}

	var result strings.Builder
	result.WriteString("Active agents:\n")

	for _, agent := range s.agents {
		// Check if tmux session still exists
		cmd := exec.Command("tmux", "has-session", "-t", agent.SessionID)
		status := "active"
		if err := cmd.Run(); err != nil {
			status = "inactive"
		}

		result.WriteString(fmt.Sprintf("- %s (Session: %s, Status: %s, Task: %s, Created: %s)\n",
			agent.ID, agent.SessionID, status, agent.Task, agent.CreatedAt.Format("15:04:05")))
	}

	return result.String(), nil
}

func (s *MCPServer) sendMessage(args map[string]interface{}) (string, error) {
	agentID, ok := args["agent_id"].(string)
	if !ok || agentID == "" {
		return "", fmt.Errorf("agent_id is required")
	}

	message, ok := args["message"].(string)
	if !ok || message == "" {
		return "", fmt.Errorf("message is required")
	}

	s.mu.RLock()
	agent, exists := s.agents[agentID]
	s.mu.RUnlock()

	if !exists {
		return "", fmt.Errorf("agent %s not found", agentID)
	}

	// Send message to tmux session
	cmd := exec.Command("tmux", "send-keys", "-t", agent.SessionID, message, "Enter")
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("failed to send message to agent %s: %w", agentID, err)
	}

	// Update last active time
	s.mu.Lock()
	agent.LastActive = time.Now()
	s.agents[agentID] = agent
	s.mu.Unlock()

	return fmt.Sprintf("Message sent to agent %s: %s", agentID, message), nil
}

func (s *MCPServer) getAgentOutput(args map[string]interface{}) (string, error) {
	agentID, ok := args["agent_id"].(string)
	if !ok || agentID == "" {
		return "", fmt.Errorf("agent_id is required")
	}

	lines := 50
	if l, ok := args["lines"].(float64); ok {
		lines = int(l)
	}

	s.mu.RLock()
	agent, exists := s.agents[agentID]
	s.mu.RUnlock()

	if !exists {
		return "", fmt.Errorf("agent %s not found", agentID)
	}

	// Capture tmux pane output
	cmd := exec.Command("tmux", "capture-pane", "-t", agent.SessionID, "-p", "-S", fmt.Sprintf("-%d", lines))
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to capture output from agent %s: %w", agentID, err)
	}

	return fmt.Sprintf("Output from agent %s (last %d lines):\n%s", agentID, lines, string(output)), nil
}

// Main server loop
func (s *MCPServer) Run() error {
	log.Printf("Starting claude-squad MCP server...")

	// Set up logging to stderr so it doesn't interfere with JSON-RPC on stdout
	log.SetOutput(os.Stderr)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	for {
		var request JSONRPCRequest
		if err := s.decoder.Decode(&request); err != nil {
			if err == io.EOF {
				log.Printf("Client disconnected")
				break
			}
			log.Printf("Error decoding request: %v", err)
			s.sendError(nil, ParseError, "Failed to parse JSON")
			continue
		}

		log.Printf("Received request: %s", request.Method)

		if request.JSONRPC != "2.0" {
			s.sendError(request.ID, InvalidRequest, "Only JSON-RPC 2.0 is supported")
			continue
		}

		switch request.Method {
		case "initialize":
			s.handleInitialize(request.ID, request.Params)
		case "initialized":
			log.Printf("Server initialized successfully")
			// No response needed for notifications
		case "tools/list":
			s.handleToolsList(request.ID)
		case "tools/call":
			s.handleToolsCall(request.ID, request.Params)
		case "resources/list":
			s.handleResourcesList(request.ID)
		case "prompts/list":
			s.handlePromptsList(request.ID)
		case "cancelled":
			if params, ok := request.Params.(map[string]interface{}); ok {
				log.Printf("Received cancellation notification for request ID: %v, reason: %v",
					params["requestId"], params["reason"])
			} else {
				log.Printf("Received cancellation notification with invalid params")
			}
			// No response needed for notifications
		default:
			s.sendError(request.ID, MethodNotFound, fmt.Sprintf("Method not implemented: %s", request.Method))
		}
	}

	log.Printf("claude-squad MCP server shutting down...")
	return nil
}
