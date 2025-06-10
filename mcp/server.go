package mcp

import (
	"claude-squad/config"
	"claude-squad/log"
	"claude-squad/session"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Agent management types
type AgentInfo struct {
	ID           string    `json:"id"`
	SessionID    string    `json:"session_id"`
	Task         string    `json:"task,omitempty"`
	Status       string    `json:"status"`
	CreatedAt    time.Time `json:"created_at"`
	LastActive   time.Time `json:"last_active"`
	IsResponding bool      `json:"is_responding"` // Track if agent is currently responding
	AutoResponse bool      `json:"auto_response"` // Enable auto-response monitoring
	AgentType    string    `json:"agent_type"`    // Type of agent (claude, research-agent, etc.)
	NonInteractive bool    `json:"non_interactive"` // Whether agent is running in --print mode
}

// Pre-configured agent types
type AgentPreset struct {
	Program     string
	SystemPrompt string
	Description string
}

type AgentManager struct {
	agents   map[string]AgentInfo
	instances map[string]*session.Instance
	storage  *session.Storage
	mu      sync.RWMutex
	responseMonitors map[string]chan struct{} // For stopping monitoring goroutines
	lastNotifications map[string]string // Track last notification sent per agent to prevent duplicates
	lastNotificationTime map[string]time.Time // Track when last notification was sent
	presets map[string]AgentPreset // Pre-configured agent types
	finalOutputs map[string]string // Store final outputs for completed agents
}

func NewAgentManager() *AgentManager {
	// Initialize storage to share with main claude-squad UI
	appState := config.LoadState()
	storage, err := session.NewStorage(appState)
	if err != nil {
		log.ErrorLog.Printf("Failed to initialize MCP storage: %v", err)
		return nil
	}

	am := &AgentManager{
		agents:    make(map[string]AgentInfo),
		instances: make(map[string]*session.Instance),
		storage:   storage,
		responseMonitors: make(map[string]chan struct{}),
		lastNotifications: make(map[string]string),
		lastNotificationTime: make(map[string]time.Time),
		presets: initializeAgentPresets(),
		finalOutputs: make(map[string]string),
	}

	// Load existing instances from storage
	if err := am.loadInstancesFromStorage(); err != nil {
		log.ErrorLog.Printf("Failed to load existing instances: %v", err)
	}

	return am
}

// initializeAgentPresets defines the pre-configured agent types
func initializeAgentPresets() map[string]AgentPreset {
	return map[string]AgentPreset{
		"claude": {
			Program:     "node /Users/conrad/Documents/github/claude-squad/claude-yolo-silent.mjs",
			SystemPrompt: "",
			Description: "Standard Claude agent for general tasks",
		},
		"research-agent": {
			Program:     "node /Users/conrad/Documents/github/claude-squad/claude-yolo-silent.mjs",
			SystemPrompt: "You are a specialized research agent. Your role is to:\n1. Thoroughly research topics using all available information\n2. Provide comprehensive, well-sourced findings\n3. Structure your research with clear sections: Overview, Key Findings, Sources, Recommendations\n4. Be methodical and analytical in your approach\n5. Always fact-check and provide multiple perspectives when relevant\n6. Format your final research report clearly with headers and bullet points",
			Description: "Specialized agent for comprehensive research tasks",
		},
		"coding-agent": {
			Program:     "node /Users/conrad/Documents/github/claude-squad/claude-yolo-silent.mjs",
			SystemPrompt: "You are a specialized coding agent. Focus on:\n1. Writing clean, efficient, well-documented code\n2. Following best practices and industry standards\n3. Implementing proper error handling and testing\n4. Explaining your code decisions clearly\n5. Suggesting improvements and optimizations\n6. Being thorough in code reviews",
			Description: "Specialized agent for coding tasks using Aider with Claude Sonnet",
		},
		"analysis-agent": {
			Program:     "node /Users/conrad/Documents/github/claude-squad/claude-yolo-silent.mjs",
			SystemPrompt: "You are a data analysis and insights agent. Your expertise includes:\n1. Breaking down complex problems into analyzable components\n2. Identifying patterns, trends, and anomalies in data\n3. Providing clear, actionable insights and recommendations\n4. Creating structured analysis reports with executive summaries\n5. Visualizing data relationships when relevant\n6. Ensuring conclusions are supported by evidence",
			Description: "Specialized agent for data analysis and insights",
		},
	}
}

// CreateMCPServer creates and configures the MCP server with agent management tools
func CreateMCPServer() *server.MCPServer {
	agentManager := NewAgentManager()
	if agentManager == nil {
		log.ErrorLog.Printf("Failed to create agent manager")
		return nil
	}

	// Create the MCP server
	s := server.NewMCPServer(
		"claude-squad",
		"1.0.0",
		server.WithToolCapabilities(true),
		server.WithRecovery(),
	)

	// Add launch_agent tool
	launchAgentTool := mcp.NewTool("launch_agent",
		mcp.WithDescription("Launch a new agent in a tmux session. The agent will automatically send responses back to the orchestrator when it completes tasks. Use 'agent_type' for pre-configured agents or 'program' for custom commands."),
		mcp.WithString("task",
			mcp.Required(),
			mcp.Description("Description of the task for the new agent"),
		),
		mcp.WithString("agent_type",
			mcp.Description("Pre-configured agent type: claude, claude-yolo, research-agent, coding-agent, analysis-agent"),
		),
		mcp.WithString("program",
			mcp.Description("Custom program to run (overrides agent_type if both provided)"),
		),
		mcp.WithString("interactive",
			mcp.Description("Set to 'true' to use interactive mode instead of default --print mode (preserves current auto-response tracking)"),
		),
	)

	// Add list_agent_types tool
	listAgentTypesTool := mcp.NewTool("list_agent_types",
		mcp.WithDescription("List all available pre-configured agent types with their descriptions"),
	)

	s.AddTool(launchAgentTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		task, err := request.RequireString("task")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		agentType := request.GetString("agent_type", "")
		program := request.GetString("program", "")
		interactive := request.GetString("interactive", "false") == "true"

		result, err := agentManager.launchAgent(task, agentType, program, interactive)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		return mcp.NewToolResultText(result), nil
	})

	s.AddTool(listAgentTypesTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		result := agentManager.listAgentTypes()
		return mcp.NewToolResultText(result), nil
	})

	// Add list_agents tool
	listAgentsTool := mcp.NewTool("list_agents",
		mcp.WithDescription("List all active agents and their status"),
	)

	s.AddTool(listAgentsTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		result := agentManager.listAgents()
		return mcp.NewToolResultText(result), nil
	})

	// Add send_message tool
	sendMessageTool := mcp.NewTool("send_message",
		mcp.WithDescription("Send a message to a specific agent via tmux"),
		mcp.WithString("agent_id",
			mcp.Required(),
			mcp.Description("ID of the target agent"),
		),
		mcp.WithString("message",
			mcp.Required(),
			mcp.Description("Message to send to the agent"),
		),
	)

	s.AddTool(sendMessageTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		agentID, err := request.RequireString("agent_id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		message, err := request.RequireString("message")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		result, err := agentManager.sendMessage(agentID, message)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		return mcp.NewToolResultText(result), nil
	})

	// Add get_agent_output tool
	getOutputTool := mcp.NewTool("get_agent_output",
		mcp.WithDescription("Get the last agent message or recent output from an agent's tmux session"),
		mcp.WithString("agent_id",
			mcp.Required(),
			mcp.Description("ID of the agent to get output from"),
		),
		mcp.WithString("mode",
			mcp.Description("Output mode: 'last_message' (default), 'last_x_messages', or 'raw_lines'"),
		),
		mcp.WithNumber("count",
			mcp.Description("Number of messages/lines to retrieve (default: 1 for messages, 50 for raw lines)"),
		),
	)

	s.AddTool(getOutputTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		agentID, err := request.RequireString("agent_id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		mode := request.GetString("mode", "last_message")
		count := int(request.GetFloat("count", 1))

		// Set default count based on mode
		if mode == "raw_lines" && count == 1 {
			count = 50
		}

		result, err := agentManager.getAgentOutput(agentID, mode, count)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		return mcp.NewToolResultText(result), nil
	})

	// Add send_to_main tool
	sendToMainTool := mcp.NewTool("send_to_main",
		mcp.WithDescription("Send agent output to main Claude session"),
		mcp.WithString("agent_id",
			mcp.Required(),
			mcp.Description("ID of the agent to send output from"),
		),
	)

	s.AddTool(sendToMainTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		agentID, err := request.RequireString("agent_id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		err = agentManager.sendOutputToMain(agentID)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		return mcp.NewToolResultText(fmt.Sprintf("Sent output from agent %s to main session", agentID)), nil
	})

	// Add debug_tmux tool for troubleshooting
	debugTmuxTool := mcp.NewTool("debug_tmux",
		mcp.WithDescription("Debug tmux sessions - list all sessions and check agent session status"),
	)

	s.AddTool(debugTmuxTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		result := agentManager.debugTmuxSessions()
		return mcp.NewToolResultText(result), nil
	})

	// Add check_ui_state tool for troubleshooting UI issues
	checkUITool := mcp.NewTool("check_ui_state",
		mcp.WithDescription("Check the current state of the claude-squad UI to debug instance creation issues"),
	)

	s.AddTool(checkUITool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		result := agentManager.checkUIState()
		return mcp.NewToolResultText(result), nil
	})

	// Add force_clean_agents tool to remove persistent agents
	cleanAgentsTool := mcp.NewTool("force_clean_agents",
		mcp.WithDescription("Force remove all MCP agents from storage and tracking to fix persistence issues"),
	)

	s.AddTool(cleanAgentsTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		result, err := agentManager.forceCleanAgents()
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(result), nil
	})


	// Add delete_agent tool
	deleteAgentTool := mcp.NewTool("delete_agent",
		mcp.WithDescription("Delete an agent and clean up its tmux session and worktree"),
		mcp.WithString("agent_id",
			mcp.Required(),
			mcp.Description("ID of the agent to delete"),
		),
	)

	s.AddTool(deleteAgentTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		agentID, err := request.RequireString("agent_id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		result, err := agentManager.deleteAgent(agentID)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		return mcp.NewToolResultText(result), nil
	})

	return s
}

// Agent management methods
func (am *AgentManager) launchAgent(task, agentType, program string, interactive bool) (string, error) {
	// Generate unique agent ID and title
	agentID := fmt.Sprintf("agent-%d", time.Now().Unix())
	title := fmt.Sprintf("Agent-%s", agentID)

	// Determine program and system prompt from agent type or custom program
	var actualProgram string
	var systemPrompt string
	var finalAgentType string
	
	if program != "" && program != "claude-squad" && program != "./claude-squad" {
		// Custom program provided - use it directly
		actualProgram = program
		systemPrompt = ""
		finalAgentType = "custom"
	} else if agentType != "" {
		// Use pre-configured agent type
		if preset, exists := am.presets[agentType]; exists {
			baseProgram := preset.Program
			systemPrompt = preset.SystemPrompt
			finalAgentType = agentType
			
			// For now, always use interactive mode since --print mode is having issues
			// We'll send the system prompt as part of the task instead
			actualProgram = baseProgram
		} else {
			return "", fmt.Errorf("unknown agent type: %s", agentType)
		}
	} else {
		// Default to standard claude
		actualProgram = "node /Users/conrad/Documents/github/claude-squad/claude-yolo-silent.mjs"
		systemPrompt = ""
		finalAgentType = "claude --dangerously-skip-permissions"
	}
	
	log.InfoLog.Printf("Creating agent %s (type: %s) with program '%s'", agentID, finalAgentType, actualProgram)
	
	// Create instance exactly like the UI does - path is automatically handled
	// This ensures compatibility with the UI's instance management
	instance, err := session.NewInstance(session.InstanceOptions{
		Title:   "",  // Start with empty title like UI
		Program: actualProgram,
	})
	if err != nil {
		return "", fmt.Errorf("failed to create agent instance: %w", err)
	}

	// Set the title (like the UI does after user input)
	if err := instance.SetTitle(title); err != nil {
		return "", fmt.Errorf("failed to set agent title: %w", err)
	}

	// Start the instance with git worktree creation
	if err := instance.Start(true); err != nil {
		return "", fmt.Errorf("failed to start agent instance: %w", err)
	}

	// Store agent info for tracking
	am.mu.Lock()
	am.agents[agentID] = AgentInfo{
		ID:         agentID,
		SessionID:  title, // Use title as session identifier
		Task:       task,
		Status:     "active",
		CreatedAt:  time.Now(),
		LastActive: time.Now(),
		IsResponding: false,
		AutoResponse: true, // Always enable auto-response
		AgentType:  finalAgentType,
		NonInteractive: false, // Always use interactive mode for now
	}
	am.instances[agentID] = instance
	
	// Create monitor channel and start monitoring automatically
	stopCh := make(chan struct{})
	am.responseMonitors[agentID] = stopCh
	am.mu.Unlock()
	
	// Start monitoring in background
	go am.monitorAgentResponse(agentID, stopCh)

	// Save to storage using the same method as UI
	allInstances := []*session.Instance{instance}
	// Add existing instances
	existingInstances, err := am.storage.LoadInstances()
	if err == nil {
		allInstances = append(allInstances, existingInstances...)
	}
	
	if err := am.storage.SaveInstances(allInstances); err != nil {
		log.ErrorLog.Printf("Failed to save agent to storage: %v", err)
	} else {
		log.InfoLog.Printf("Saved agent %s to storage using UI-compatible method", agentID)
	}

	// Wait longer for the claude-yolo wrapper to fully initialize 
	// The wrapper needs time to modify the CLI and start properly
	time.Sleep(8 * time.Second)
	
	// Send the task text first, then Enter separately (SendPrompt's Enter doesn't seem to work)
	if task != "" {
		var enhancedTask string
		if systemPrompt != "" {
			enhancedTask = fmt.Sprintf(`%s

TASK: %s

IMPORTANT: This agent is being monitored automatically. When you complete your task, your response will be automatically sent back to the orchestrator. No additional action is needed from you - just complete the task normally.`, systemPrompt, task)
		} else {
			enhancedTask = fmt.Sprintf(`%s

IMPORTANT: This agent is being monitored automatically. When you complete your task, your response will be automatically sent back to the orchestrator. No additional action is needed from you - just complete the task normally.`, task)
		}
		
		log.InfoLog.Printf("Sending text to agent %s", agentID)
		// Send just the text without Enter using SendKeys directly
		sessionName := formatSessionName(title)
		cmd1 := exec.Command("tmux", "send-keys", "-t", sessionName, enhancedTask)
		if err := cmd1.Run(); err != nil {
			log.ErrorLog.Printf("Failed to send text to agent %s: %v", agentID, err)
		} else {
			log.InfoLog.Printf("Successfully sent text to agent %s", agentID)
		}
		
		time.Sleep(1 * time.Second)
		
		log.InfoLog.Printf("Sending Enter to agent %s", agentID)
		// Now send Enter separately  
		cmd2 := exec.Command("tmux", "send-keys", "-t", sessionName, "C-m")
		if err := cmd2.Run(); err != nil {
			log.ErrorLog.Printf("Failed to send Enter to agent %s: %v", agentID, err)
		} else {
			log.InfoLog.Printf("Successfully sent Enter to agent %s", agentID)
		}
		
		time.Sleep(2 * time.Second)
		log.InfoLog.Printf("Agent %s should now be processing the task", agentID)
	}

	return fmt.Sprintf("Agent %s created with auto-response monitoring enabled. Task: %s\n\nThe agent will automatically send responses back to the orchestrator when it completes tasks.", agentID, task), nil
}

func (am *AgentManager) listAgents() string {
	am.mu.RLock()
	defer am.mu.RUnlock()

	if len(am.agents) == 0 {
		return "No active agents"
	}

	var result strings.Builder
	result.WriteString("Active agents:\n")

	for _, agent := range am.agents {
		// Check if tmux session still exists using formatted session name
		sessionName := formatSessionName(agent.SessionID)
		cmd := exec.Command("tmux", "has-session", "-t", sessionName)
		status := "active"
		if err := cmd.Run(); err != nil {
			status = "inactive"
		}

		mode := "non-interactive"
		if !agent.NonInteractive {
			mode = "interactive"
		}
		result.WriteString(fmt.Sprintf("- %s (Type: %s, Mode: %s, Session: %s, Status: %s, Task: %s, Created: %s)\n",
			agent.ID, agent.AgentType, mode, sessionName, status, agent.Task, agent.CreatedAt.Format("15:04:05")))
	}

	return result.String()
}

func (am *AgentManager) listAgentTypes() string {
	var result strings.Builder
	result.WriteString("Available pre-configured agent types:\n\n")
	
	for typeName, preset := range am.presets {
		result.WriteString(fmt.Sprintf("• %s: %s\n", typeName, preset.Description))
		result.WriteString(fmt.Sprintf("  Program: %s\n", preset.Program))
		if preset.SystemPrompt != "" {
			// Show first line of system prompt
			lines := strings.Split(preset.SystemPrompt, "\n")
			result.WriteString(fmt.Sprintf("  System Prompt: %s...\n", lines[0]))
		}
		result.WriteString("\n")
	}
	
	result.WriteString("Usage: Use 'agent_type' parameter in launch_agent with one of the types above.\n")
	result.WriteString("Example: {\"agent_type\": \"research-agent\", \"task\": \"Research AI safety\"}")
	
	return result.String()
}

// formatSessionName converts a title to the tmux session name used by claude-squad
func formatSessionName(title string) string {
	return fmt.Sprintf("claudesquad_%s", strings.ReplaceAll(title, " ", ""))
}

func (am *AgentManager) sendMessage(agentID, message string) (string, error) {
	am.mu.RLock()
	agent, exists := am.agents[agentID]
	am.mu.RUnlock()

	if !exists {
		return "", fmt.Errorf("agent %s not found", agentID)
	}

	instance, ok := am.instances[agentID]
	if !ok {
		return "", fmt.Errorf("agent %s has no instance", agentID)
	}

	// Send message using claude-squad instance tmux session (two-step: text first, then enter)
	// Access the tmux session directly from the instance
	if !instance.Started() {
		return "", fmt.Errorf("agent %s instance not started", agentID)
	}

	// Use the formatted session name
	sessionName := formatSessionName(instance.Title)

	// First verify the session exists
	checkCmd := exec.Command("tmux", "has-session", "-t", sessionName)
	if err := checkCmd.Run(); err != nil {
		return "", fmt.Errorf("agent %s session %s does not exist", agentID, sessionName)
	}

	cmd1 := exec.Command("tmux", "send-keys", "-t", sessionName, message)
	if err := cmd1.Run(); err != nil {
		return "", fmt.Errorf("failed to send message to agent %s (session: %s): %w", agentID, sessionName, err)
	}

	cmd2 := exec.Command("tmux", "send-keys", "-t", sessionName, "C-m")
	if err := cmd2.Run(); err != nil {
		return "", fmt.Errorf("failed to send enter to agent %s (session: %s): %w", agentID, sessionName, err)
	}

	// Update last active time
	am.mu.Lock()
	agent.LastActive = time.Now()
	am.agents[agentID] = agent
	am.mu.Unlock()

	return fmt.Sprintf("Message sent to agent %s: %s", agentID, message), nil
}

func (am *AgentManager) getAgentOutput(agentID string, mode string, count int) (string, error) {
	am.mu.RLock()
	agent, exists := am.agents[agentID]
	finalOutput, hasFinalOutput := am.finalOutputs[agentID]
	am.mu.RUnlock()

	if !exists {
		return "", fmt.Errorf("agent %s not found", agentID)
	}

	// If agent is non-interactive and we have final output stored, use that instead of tmux
	if agent.NonInteractive && hasFinalOutput {
		switch mode {
		case "last_message":
			return am.parseLastMessageFromOutput(finalOutput, agentID)
		case "last_x_messages":
			return am.parseLastXMessagesFromOutput(finalOutput, agentID, count)
		default:
			return fmt.Sprintf("Final output from agent %s:\\n%s", agentID, finalOutput), nil
		}
	}

	// Use the formatted session name (same as sendMessage)
	sessionName := formatSessionName(agent.SessionID)

	switch mode {
	case "last_message":
		return am.getLastAgentMessage(sessionName, agentID)
	case "last_x_messages":
		return am.getLastXAgentMessages(sessionName, agentID, count)
	case "raw_lines":
		fallthrough
	default:
		// First verify the session exists
		checkCmd := exec.Command("tmux", "has-session", "-t", sessionName)
		if err := checkCmd.Run(); err != nil {
			return "", fmt.Errorf("agent %s session %s does not exist", agentID, sessionName)
		}
		
		// Capture tmux pane output
		cmd := exec.Command("tmux", "capture-pane", "-t", sessionName, "-p", "-S", fmt.Sprintf("-%d", count))
		output, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("failed to capture output from agent %s (session: %s): %w", agentID, sessionName, err)
		}
		return fmt.Sprintf("Output from agent %s (last %d lines):\n%s", agentID, count, string(output)), nil
	}
}

// getLastAgentMessage extracts the last complete message from the agent
func (am *AgentManager) getLastAgentMessage(sessionName, agentID string) (string, error) {
	// First verify the session exists
	checkCmd := exec.Command("tmux", "has-session", "-t", sessionName)
	if err := checkCmd.Run(); err != nil {
		return "", fmt.Errorf("agent %s session %s does not exist", agentID, sessionName)
	}
	
	// Get more lines to ensure we capture complete messages
	cmd := exec.Command("tmux", "capture-pane", "-t", sessionName, "-p", "-S", "-200")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to capture output from agent %s (session: %s): %w", agentID, sessionName, err)
	}

	lines := strings.Split(string(output), "\n")
	
	// Find the last substantive agent message by looking for patterns
	var messages []string
	var currentMessage strings.Builder
	inMessage := false
	
	for _, line := range lines {
		line = strings.TrimSpace(line)
		
		// Skip empty lines
		if line == "" {
			continue
		}
		
		// Skip prompts and UI elements
		if strings.HasPrefix(line, ">") || 
		   strings.HasPrefix(line, "?") ||
		   strings.Contains(line, "for shortcuts") ||
		   strings.Contains(line, "╭") ||
		   strings.Contains(line, "╰") ||
		   strings.Contains(line, "│") {
			// If we were building a message, save it
			if inMessage && currentMessage.Len() > 0 {
				messages = append(messages, strings.TrimSpace(currentMessage.String()))
				currentMessage.Reset()
				inMessage = false
			}
			continue
		}
		
		// This looks like actual content, start/continue building message
		if !inMessage {
			inMessage = true
		}
		
		if currentMessage.Len() > 0 {
			currentMessage.WriteString("\n")
		}
		currentMessage.WriteString(line)
	}
	
	// Add final message if we have one
	if inMessage && currentMessage.Len() > 0 {
		messages = append(messages, strings.TrimSpace(currentMessage.String()))
	}
	
	// Return the last meaningful message
	if len(messages) == 0 {
		return fmt.Sprintf("No recent substantive message found from agent %s", agentID), nil
	}
	
	lastMsg := messages[len(messages)-1]
	
	// Filter out very short or non-substantive messages
	if len(lastMsg) < 10 || 
	   strings.Contains(strings.ToLower(lastMsg), "trust") ||
	   strings.Contains(strings.ToLower(lastMsg), "file") && strings.Contains(strings.ToLower(lastMsg), "folder") {
		// Look for a better message
		for i := len(messages) - 2; i >= 0; i-- {
			candidate := messages[i]
			if len(candidate) >= 10 && 
			   !strings.Contains(strings.ToLower(candidate), "trust") &&
			   !(strings.Contains(strings.ToLower(candidate), "file") && strings.Contains(strings.ToLower(candidate), "folder")) {
				lastMsg = candidate
				break
			}
		}
	}
	
	return fmt.Sprintf("Last message from agent %s:\n%s", agentID, lastMsg), nil
}

// getLastXAgentMessages extracts the last X complete messages from the agent
func (am *AgentManager) getLastXAgentMessages(sessionName, agentID string, count int) (string, error) {
	// First verify the session exists
	checkCmd := exec.Command("tmux", "has-session", "-t", sessionName)
	if err := checkCmd.Run(); err != nil {
		return "", fmt.Errorf("agent %s session %s does not exist", agentID, sessionName)
	}
	
	// Get more lines to ensure we capture complete messages
	cmd := exec.Command("tmux", "capture-pane", "-t", sessionName, "-p", "-S", "-300")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to capture output from agent %s (session: %s): %w", agentID, sessionName, err)
	}

	lines := strings.Split(string(output), "\n")
	var messages []string
	var currentMessage strings.Builder
	inMessage := false
	
	for _, line := range lines {
		line = strings.TrimSpace(line)
		
		// Skip empty lines
		if line == "" {
			continue
		}
		
		// Skip prompts and UI elements - same logic as getLastAgentMessage
		if strings.HasPrefix(line, ">") || 
		   strings.HasPrefix(line, "?") ||
		   strings.Contains(line, "for shortcuts") ||
		   strings.Contains(line, "╭") ||
		   strings.Contains(line, "╰") ||
		   strings.Contains(line, "│") {
			// If we were building a message, save it
			if inMessage && currentMessage.Len() > 0 {
				msg := strings.TrimSpace(currentMessage.String())
				// Only save substantial messages
				if len(msg) >= 10 && 
				   !strings.Contains(strings.ToLower(msg), "trust") &&
				   !(strings.Contains(strings.ToLower(msg), "file") && strings.Contains(strings.ToLower(msg), "folder")) {
					messages = append(messages, msg)
				}
				currentMessage.Reset()
				inMessage = false
			}
			continue
		}
		
		// This looks like actual content, start/continue building message
		if !inMessage {
			inMessage = true
		}
		
		if currentMessage.Len() > 0 {
			currentMessage.WriteString("\n")
		}
		currentMessage.WriteString(line)
	}
	
	// Add final message if we have one
	if inMessage && currentMessage.Len() > 0 {
		msg := strings.TrimSpace(currentMessage.String())
		if len(msg) >= 10 && 
		   !strings.Contains(strings.ToLower(msg), "trust") &&
		   !(strings.Contains(strings.ToLower(msg), "file") && strings.Contains(strings.ToLower(msg), "folder")) {
			messages = append(messages, msg)
		}
	}
	
	// Get the last 'count' messages
	start := len(messages) - count
	if start < 0 {
		start = 0
	}
	
	if len(messages) == 0 {
		return fmt.Sprintf("No recent substantive messages found from agent %s", agentID), nil
	}
	
	selectedMessages := messages[start:]
	result := strings.Join(selectedMessages, "\n\n--- Next Message ---\n\n")
	
	return fmt.Sprintf("Last %d message(s) from agent %s:\n%s", len(selectedMessages), agentID, result), nil
}

// Send agent output back to main Claude session
func (am *AgentManager) sendOutputToMain(agentID string) error {
	// Get agent output using the new last_message mode
	output, err := am.getAgentOutput(agentID, "last_message", 1)
	if err != nil {
		return err
	}

	// Send to main session (claudesquad_orc)
	cmd1 := exec.Command("tmux", "send-keys", "-t", "claudesquad_orc", fmt.Sprintf("Agent %s output: %s", agentID, output))
	if err := cmd1.Run(); err != nil {
		return fmt.Errorf("failed to send output to main: %w", err)
	}

	cmd2 := exec.Command("tmux", "send-keys", "-t", "claudesquad_orc", "C-m")
	if err := cmd2.Run(); err != nil {
		return fmt.Errorf("failed to send enter to main: %w", err)
	}

	return nil
}

// debugTmuxSessions provides debug information about tmux sessions
func (am *AgentManager) debugTmuxSessions() string {
	var result strings.Builder
	result.WriteString("=== TMUX SESSION DEBUG ===\n\n")
	
	// List all tmux sessions
	cmd := exec.Command("tmux", "list-sessions")
	output, err := cmd.Output()
	if err != nil {
		result.WriteString(fmt.Sprintf("Failed to list tmux sessions: %v\n", err))
	} else {
		result.WriteString("All tmux sessions:\n")
		result.WriteString(string(output))
		result.WriteString("\n")
	}
	
	// Check each tracked agent
	am.mu.RLock()
	result.WriteString(fmt.Sprintf("Tracked agents: %d\n", len(am.agents)))
	for agentID, agent := range am.agents {
		sessionName := formatSessionName(agent.SessionID)
		result.WriteString(fmt.Sprintf("\nAgent: %s\n", agentID))
		result.WriteString(fmt.Sprintf("  Title: %s\n", agent.SessionID))
		result.WriteString(fmt.Sprintf("  Expected session: %s\n", sessionName))
		
		// Check if session exists
		checkCmd := exec.Command("tmux", "has-session", "-t", sessionName)
		if err := checkCmd.Run(); err != nil {
			result.WriteString(fmt.Sprintf("  Status: ❌ Session does not exist\n"))
		} else {
			result.WriteString(fmt.Sprintf("  Status: ✅ Session exists\n"))
			
			// Try to get session info
			infoCmd := exec.Command("tmux", "display-message", "-t", sessionName, "-p", "#{session_name}:#{window_name}:#{pane_current_command}")
			if infoOutput, err := infoCmd.Output(); err == nil {
				result.WriteString(fmt.Sprintf("  Info: %s\n", strings.TrimSpace(string(infoOutput))))
			}
		}
	}
	am.mu.RUnlock()
	
	return result.String()
}

// checkUIState captures the current claude-squad UI state for debugging
func (am *AgentManager) checkUIState() string {
	var result strings.Builder
	result.WriteString("=== CLAUDE-SQUAD UI STATE ===\n\n")
	
	// Check if the main session exists
	checkCmd := exec.Command("tmux", "has-session", "-t", "claudesquad_orc")
	if err := checkCmd.Run(); err != nil {
		result.WriteString("❌ Main claude-squad session 'claudesquad_orc' does not exist\n")
		return result.String()
	}
	
	result.WriteString("✅ Main claude-squad session 'claudesquad_orc' exists\n\n")
	
	// Capture current screen content
	captureCmd := exec.Command("tmux", "capture-pane", "-t", "claudesquad_orc", "-p")
	output, err := captureCmd.Output()
	if err != nil {
		result.WriteString(fmt.Sprintf("Failed to capture UI state: %v\n", err))
	} else {
		result.WriteString("Current UI content:\n")
		result.WriteString("---\n")
		result.WriteString(string(output))
		result.WriteString("---\n\n")
	}
	
	// Get session info
	infoCmd := exec.Command("tmux", "display-message", "-t", "claudesquad_orc", "-p", 
		"Session: #{session_name}, Window: #{window_name}, Pane: #{pane_current_command}")
	if infoOutput, err := infoCmd.Output(); err == nil {
		result.WriteString(fmt.Sprintf("Session info: %s\n", strings.TrimSpace(string(infoOutput))))
	}
	
	return result.String()
}

// saveInstancesToStorage saves all current instances to the shared storage
func (am *AgentManager) saveInstancesToStorage() error {
	am.mu.RLock()
	defer am.mu.RUnlock()

	log.InfoLog.Printf("Saving %d MCP instances to storage", len(am.instances))

	// Load existing instances to merge with MCP instances
	existingInstances, err := am.storage.LoadInstances()
	if err != nil {
		return fmt.Errorf("failed to load existing instances: %w", err)
	}
	log.InfoLog.Printf("Loaded %d existing instances from storage", len(existingInstances))

	// Create a map of existing non-MCP instances
	// Also preserve MCP instances that are in storage but NOT currently tracked
	// (This means they were deleted and shouldn't be re-added)
	nonMCPInstances := make(map[string]*session.Instance)
	for _, instance := range existingInstances {
		if !strings.HasPrefix(instance.Title, "Agent-agent-") {
			// Regular non-MCP instance - always preserve
			nonMCPInstances[instance.Title] = instance
		} else {
			// MCP instance - only preserve if we're NOT currently tracking it
			// (If we're not tracking it, it was probably deleted)
			found := false
			for _, trackedInstance := range am.instances {
				if trackedInstance.Title == instance.Title {
					found = true
					break
				}
			}
			if !found {
				// This MCP instance is not being tracked, so it was deleted - don't preserve it
				log.InfoLog.Printf("Not preserving deleted MCP instance: %s", instance.Title)
			}
		}
	}
	log.InfoLog.Printf("Found %d non-MCP instances to preserve", len(nonMCPInstances))

	// Combine non-MCP instances with current MCP instances
	var allInstances []*session.Instance
	
	// Add non-MCP instances
	for _, instance := range nonMCPInstances {
		allInstances = append(allInstances, instance)
	}
	
	// Add current MCP instances
	for _, instance := range am.instances {
		allInstances = append(allInstances, instance)
		log.InfoLog.Printf("Adding MCP instance to storage: %s (started: %v)", instance.Title, instance.Started())
	}

	log.InfoLog.Printf("Saving total of %d instances to storage", len(allInstances))
	err = am.storage.SaveInstances(allInstances)
	if err != nil {
		log.ErrorLog.Printf("Failed to save instances to storage: %v", err)
		return err
	}
	log.InfoLog.Printf("Successfully saved all instances to storage")
	return nil
}

// loadInstancesFromStorage loads existing instances from shared storage
func (am *AgentManager) loadInstancesFromStorage() error {
	instances, err := am.storage.LoadInstances()
	if err != nil {
		return fmt.Errorf("failed to load instances: %w", err)
	}

	am.mu.Lock()
	defer am.mu.Unlock()

	// Only load instances that look like they were created by MCP (have Agent- prefix)
	// BUT don't reload agents that were already being tracked (they might have been deleted)
	for _, instance := range instances {
		if strings.HasPrefix(instance.Title, "Agent-agent-") {
			// Extract agent ID from title (format: Agent-agent-1234567890)
			parts := strings.Split(instance.Title, "-")
			if len(parts) >= 3 {
				agentID := strings.Join(parts[1:], "-") // agent-1234567890
				
				// Only load if we're not already tracking this agent
				// This prevents deleted agents from being automatically reloaded
				if _, exists := am.agents[agentID]; !exists {
					am.instances[agentID] = instance
					am.agents[agentID] = AgentInfo{
						ID:         agentID,
						SessionID:  instance.Title,
						Task:       "", // Task info is lost on reload
						Status:     "active",
						CreatedAt:  instance.CreatedAt,
						LastActive: instance.UpdatedAt,
						IsResponding: false,
						AutoResponse: true, // Always enable auto-response
						AgentType:  "unknown", // Type info is lost on reload
						NonInteractive: true, // Default to non-interactive mode for reloaded agents
					}
					
					// Start monitoring for existing agents too
					stopCh := make(chan struct{})
					am.responseMonitors[agentID] = stopCh
					go am.monitorAgentResponse(agentID, stopCh)
					
					log.InfoLog.Printf("Loaded agent %s from storage with auto-monitoring", agentID)
				} else {
					log.InfoLog.Printf("Skipping reload of already tracked agent %s", agentID)
				}
			}
		}
	}

	return nil
}

// deleteAgent removes an agent using the same logic as the UI 'D' key
func (am *AgentManager) deleteAgent(agentID string) (string, error) {
	am.mu.Lock()
	defer am.mu.Unlock()

	// Check if agent exists
	agent, exists := am.agents[agentID]
	if !exists {
		return "", fmt.Errorf("agent %s not found", agentID)
	}

	// Get the instance
	instance, hasInstance := am.instances[agentID]
	if !hasInstance || instance == nil {
		return "", fmt.Errorf("agent %s has no instance", agentID)
	}

	// Use the same deletion logic as the UI 'D' key:
	// 1. Delete from storage first
	if err := am.storage.DeleteInstance(agent.SessionID); err != nil {
		return "", fmt.Errorf("failed to delete agent %s from storage: %w", agentID, err)
	}

	// 2. Kill the instance (same as m.list.Kill())
	if err := instance.Kill(); err != nil {
		log.ErrorLog.Printf("Failed to kill instance for agent %s: %v", agentID, err)
		// Continue with cleanup even if kill fails
	}

	// Stop monitoring if running
	if stopCh, exists := am.responseMonitors[agentID]; exists {
		close(stopCh)
		delete(am.responseMonitors, agentID)
	}

	// Clean up notification tracking
	delete(am.lastNotifications, agentID)
	delete(am.lastNotificationTime, agentID)

	// Remove from in-memory tracking
	delete(am.agents, agentID)
	delete(am.instances, agentID)

	log.InfoLog.Printf("Deleted agent %s using UI deletion logic", agentID)
	return fmt.Sprintf("Agent %s (session: %s) has been deleted successfully", agentID, agent.SessionID), nil
}

// forceCleanAgents removes all MCP agents from both tracking and storage
func (am *AgentManager) forceCleanAgents() (string, error) {
	am.mu.Lock()
	defer am.mu.Unlock()

	// Kill all tracked agent instances
	var killedAgents []string
	for agentID, instance := range am.instances {
		if instance != nil {
			if err := instance.Kill(); err != nil {
				log.ErrorLog.Printf("Failed to kill instance for agent %s: %v", agentID, err)
			}
		}
		killedAgents = append(killedAgents, agentID)
	}

	// Clear all MCP tracking
	am.agents = make(map[string]AgentInfo)
	am.instances = make(map[string]*session.Instance)

	// Load storage and remove all MCP agents
	existingInstances, err := am.storage.LoadInstances()
	if err != nil {
		return "", fmt.Errorf("failed to load instances from storage: %w", err)
	}

	// Keep only non-MCP instances
	var cleanInstances []*session.Instance
	var removedTitles []string
	for _, instance := range existingInstances {
		if !strings.HasPrefix(instance.Title, "Agent-agent-") {
			cleanInstances = append(cleanInstances, instance)
		} else {
			removedTitles = append(removedTitles, instance.Title)
		}
	}

	// Save cleaned storage
	if err := am.storage.SaveInstances(cleanInstances); err != nil {
		return "", fmt.Errorf("failed to save cleaned instances: %w", err)
	}

	result := fmt.Sprintf("Force cleaned %d tracked agents and %d stored instances.\n", 
		len(killedAgents), len(removedTitles))
	result += fmt.Sprintf("Tracked agents removed: %v\n", killedAgents)
	result += fmt.Sprintf("Storage instances removed: %v", removedTitles)

	return result, nil
}


// monitorAgentResponse monitors an agent for completion indicators and sends responses
func (am *AgentManager) monitorAgentResponse(agentID string, stopCh chan struct{}) {
	sessionName := ""
	var isNonInteractive bool
	
	// Get session name and mode
	func() {
		am.mu.RLock()
		defer am.mu.RUnlock()
		if agent, exists := am.agents[agentID]; exists {
			sessionName = formatSessionName(agent.SessionID)
			isNonInteractive = agent.NonInteractive
		}
	}()

	if sessionName == "" {
		log.ErrorLog.Printf("Could not find session name for agent %s", agentID)
		return
	}

	mode := "non-interactive"
	if !isNonInteractive {
		mode = "interactive"
	}
	log.InfoLog.Printf("Starting auto-response monitoring for agent %s (session: %s, mode: %s)", agentID, sessionName, mode)

	ticker := time.NewTicker(2 * time.Second) // Check every 2 seconds
	defer ticker.Stop()

	var wasResponding bool
	var lastOutputHash string
	var hasSeenResult bool // For non-interactive mode
	var finalOutput string // Store final output for non-interactive mode

	for {
		select {
		case <-stopCh:
			log.InfoLog.Printf("Stopping auto-response monitoring for agent %s", agentID)
			return
		case <-ticker.C:
			// Check if session still exists
			checkCmd := exec.Command("tmux", "has-session", "-t", sessionName)
			sessionExists := checkCmd.Run() == nil
			
			if !sessionExists {
				// Session ended - for non-interactive mode, this means task completed
				if isNonInteractive {
					log.InfoLog.Printf("Agent %s session ended - task completed (non-interactive mode)", agentID)
					am.updateAgentResponseState(agentID, false)
					
					// Try to get any remaining output from tmux history before it's completely gone
					// This captures more than just the visible screen
					historyCmd := exec.Command("tmux", "capture-pane", "-t", sessionName, "-p", "-S", "-")
					if historyOutput, err := historyCmd.Output(); err == nil {
						historyStr := string(historyOutput)
						if len(historyStr) > len(finalOutput) && len(historyStr) > 1000 {
							finalOutput = historyStr
							log.InfoLog.Printf("Agent %s captured final history output (%d chars)", agentID, len(historyStr))
						}
					}
					
					// Save output to file for debugging and retrieval
					tasksDir := "/tmp/tasks"
					os.MkdirAll(tasksDir, 0755) // Create tasks directory if it doesn't exist
					
					// Use agent type and timestamp for filename
					agent, _ := am.agents[agentID]
					filename := fmt.Sprintf("%s-%s.txt", agent.AgentType, agentID)
					outputFile := fmt.Sprintf("%s/%s", tasksDir, filename)
					
					if err := os.WriteFile(outputFile, []byte(finalOutput), 0644); err == nil {
						log.InfoLog.Printf("Saved agent %s output to %s (%d chars)", agentID, outputFile, len(finalOutput))
					}
					
					// Store final output for later retrieval and send notification
					am.mu.Lock()
					am.finalOutputs[agentID] = finalOutput
					am.mu.Unlock()
					
					go func() {
						time.Sleep(500 * time.Millisecond)
						am.sendCompletionNotificationWithOutput(agentID, finalOutput)
					}()
				} else {
					log.InfoLog.Printf("Agent %s session no longer exists, stopping monitor", agentID)
				}
				return
			}

			// Capture current output
			cmd := exec.Command("tmux", "capture-pane", "-t", sessionName, "-p", "-S", "-100")
			output, err := cmd.Output()
			if err != nil {
				log.ErrorLog.Printf("Failed to capture output for agent %s: %v", agentID, err)
				continue
			}

			outputStr := string(output)
			
			// Store output for potential use when session ends
			// Only store if it contains actual results, not just the prompt we sent
			if isNonInteractive && outputStr != "" {
				// Check if this looks like actual agent output (contains result or substantial content)
				if strings.Contains(outputStr, `{"type":"result"`) || 
				   (!strings.Contains(outputStr, "IMPORTANT: This agent is being monitored") && len(outputStr) > 500) {
					finalOutput = outputStr
					log.InfoLog.Printf("Agent %s captured output (%d chars)", agentID, len(outputStr))
				}
			}
			
			// Different detection logic for interactive vs non-interactive modes
			var isResponding bool
			var isCompleted bool
			
			if isNonInteractive {
				// Non-interactive mode: look for {"type":"result" pattern for completion
				isResponding = !strings.Contains(outputStr, `{"type":"result"`)
				isCompleted = strings.Contains(outputStr, `{"type":"result"`) && !hasSeenResult
				if isCompleted {
					hasSeenResult = true
				}
			} else {
				// Interactive mode: look for "esc to interrupt" pattern
				isResponding = strings.Contains(strings.ToLower(outputStr), "esc to interrupt")
			}

			// Detect state changes
			stateChanged := false
			if !wasResponding && isResponding {
				// Agent started responding
				log.InfoLog.Printf("Agent %s started responding (%s mode)", agentID, mode)
				am.updateAgentResponseState(agentID, true)
				stateChanged = true
			} else if wasResponding && !isResponding {
				// Agent finished responding - check if output actually changed
				currentHash := am.hashString(outputStr)
				if currentHash != lastOutputHash && lastOutputHash != "" {
					log.InfoLog.Printf("Agent %s finished responding with new output (%s mode, hash: %s -> %s)", agentID, mode, lastOutputHash, currentHash)
					am.updateAgentResponseState(agentID, false)
					
					// Send response back to orchestrator with delay to avoid race conditions
					go func() {
						time.Sleep(1 * time.Second) // Brief delay to ensure state is stable
						am.sendCompletionNotification(agentID)
					}()
					stateChanged = true
				}
			} else if isNonInteractive && isCompleted {
				// Non-interactive mode: immediate completion when result appears
				log.InfoLog.Printf("Agent %s completed (non-interactive mode, found result)", agentID)
				am.updateAgentResponseState(agentID, false)
				
				// Store final output for later retrieval
				am.mu.Lock()
				am.finalOutputs[agentID] = finalOutput
				am.mu.Unlock()
				
				go func() {
					time.Sleep(1 * time.Second)
					am.sendCompletionNotificationWithOutput(agentID, finalOutput)
				}()
				stateChanged = true
				// Exit monitoring loop since task is complete
				return
			}

			if stateChanged || isResponding != wasResponding {
				lastOutputHash = am.hashString(outputStr)
			}

			wasResponding = isResponding
		}
	}
}

// updateAgentResponseState updates the agent's responding state
func (am *AgentManager) updateAgentResponseState(agentID string, isResponding bool) {
	am.mu.Lock()
	defer am.mu.Unlock()
	
	if agent, exists := am.agents[agentID]; exists {
		agent.IsResponding = isResponding
		agent.LastActive = time.Now()
		am.agents[agentID] = agent
	}
}

// sendCompletionNotificationWithOutput sends the agent's response to the orchestrator with provided output
func (am *AgentManager) sendCompletionNotificationWithOutput(agentID string, output string) {
	// Check for duplicates and cooldown period
	am.mu.Lock()
	lastNotification, notificationExists := am.lastNotifications[agentID]
	lastTime, timeExists := am.lastNotificationTime[agentID]
	
	// Prevent duplicates and rapid-fire notifications (minimum 5 seconds between notifications)
	now := time.Now()
	if notificationExists && lastNotification == output {
		am.mu.Unlock()
		log.InfoLog.Printf("Skipping exact duplicate notification for agent %s", agentID)
		return
	}
	if timeExists && now.Sub(lastTime) < 5*time.Second {
		am.mu.Unlock()
		log.InfoLog.Printf("Skipping notification for agent %s (cooldown period)", agentID)
		return
	}
	
	// Store this notification as the last one sent
	am.lastNotifications[agentID] = output
	am.lastNotificationTime[agentID] = now
	am.mu.Unlock()

	// Send to main session (orchestrator) - use two commands with delay for large outputs
	message := fmt.Sprintf("AGENT_COMPLETE:%s:%s", agentID, output)
	
	log.InfoLog.Printf("Sending completion notification text for agent %s (%d chars)", agentID, len(message))
	cmd1 := exec.Command("tmux", "send-keys", "-t", "claudesquad_orc", message)
	if err := cmd1.Run(); err != nil {
		log.ErrorLog.Printf("Failed to send completion notification to orchestrator: %v", err)
		return
	}

	// Add delay based on message size - larger messages need more time to paste
	delay := time.Duration(len(message)/1000+1) * time.Second
	if delay > 5*time.Second {
		delay = 5 * time.Second // Cap at 5 seconds max
	}
	log.InfoLog.Printf("Waiting %v before sending Enter for agent %s", delay, agentID)
	time.Sleep(delay)

	log.InfoLog.Printf("Sending Enter for completion notification for agent %s", agentID)
	cmd2 := exec.Command("tmux", "send-keys", "-t", "claudesquad_orc", "C-m")
	if err := cmd2.Run(); err != nil {
		log.ErrorLog.Printf("Failed to send enter for completion notification: %v", err)
		return
	}

	log.InfoLog.Printf("Sent completion notification for agent %s to orchestrator", agentID)
}

// sendCompletionNotification sends the agent's response to the orchestrator
func (am *AgentManager) sendCompletionNotification(agentID string) {
	// Get the agent's latest response
	output, err := am.getAgentOutput(agentID, "last_message", 1)
	if err != nil {
		log.ErrorLog.Printf("Failed to get output for completion notification from agent %s: %v", agentID, err)
		return
	}

	// Check for duplicates and cooldown period
	am.mu.Lock()
	lastNotification, notificationExists := am.lastNotifications[agentID]
	lastTime, timeExists := am.lastNotificationTime[agentID]
	
	// Prevent duplicates and rapid-fire notifications (minimum 5 seconds between notifications)
	now := time.Now()
	if notificationExists && lastNotification == output {
		am.mu.Unlock()
		log.InfoLog.Printf("Skipping exact duplicate notification for agent %s", agentID)
		return
	}
	if timeExists && now.Sub(lastTime) < 5*time.Second {
		am.mu.Unlock()
		log.InfoLog.Printf("Skipping notification for agent %s (cooldown period)", agentID)
		return
	}
	
	// Store this notification as the last one sent
	am.lastNotifications[agentID] = output
	am.lastNotificationTime[agentID] = now
	am.mu.Unlock()

	// Send to main session (orchestrator)
	message := fmt.Sprintf("AGENT_COMPLETE:%s:%s", agentID, output)
	
	cmd1 := exec.Command("tmux", "send-keys", "-t", "claudesquad_orc", message)
	if err := cmd1.Run(); err != nil {
		log.ErrorLog.Printf("Failed to send completion notification to orchestrator: %v", err)
		return
	}

	cmd2 := exec.Command("tmux", "send-keys", "-t", "claudesquad_orc", "C-m")
	if err := cmd2.Run(); err != nil {
		log.ErrorLog.Printf("Failed to send enter for completion notification: %v", err)
		return
	}

	log.InfoLog.Printf("Sent completion notification for agent %s to orchestrator", agentID)
}

// parseLastMessageFromOutput extracts the last message from stored output
func (am *AgentManager) parseLastMessageFromOutput(output, agentID string) (string, error) {
	// For non-interactive agents, the final output IS the last message
	return fmt.Sprintf("Last message from agent %s:\\n%s", agentID, output), nil
}

// parseLastXMessagesFromOutput extracts the last X messages from stored output  
func (am *AgentManager) parseLastXMessagesFromOutput(output, agentID string, count int) (string, error) {
	// For non-interactive agents, we only have the final output, so return that
	return fmt.Sprintf("Final output from agent %s:\\n%s", agentID, output), nil
}

// hashString creates a simple hash of a string for comparison
func (am *AgentManager) hashString(s string) string {
	// Remove whitespace and control characters for better comparison
	cleaned := strings.ReplaceAll(s, " ", "")
	cleaned = strings.ReplaceAll(cleaned, "\n", "")
	cleaned = strings.ReplaceAll(cleaned, "\t", "")
	return fmt.Sprintf("%x", len(cleaned)+len(s)) // Hash based on both cleaned length and original length
}
