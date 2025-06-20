package main

import (
	"claude-squad/app"
	"claude-squad/config"
	"claude-squad/daemon"
	"claude-squad/log"
	"claude-squad/mcp"
	"claude-squad/session"
	"claude-squad/session/git"
	"claude-squad/session/tmux"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"
)

var (
	version     = "1.0.0"
	programFlag string
	autoYesFlag bool
	daemonFlag  bool
	mcpFlag     bool
	rootCmd     = &cobra.Command{
		Use:   "claude-squad",
		Short: "Claude Squad - A terminal-based session manager",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			log.Initialize(daemonFlag)
			defer log.Close()

			if daemonFlag {
				cfg := config.LoadConfig()
				err := daemon.RunDaemon(cfg)
				log.ErrorLog.Printf("failed to start daemon %v", err)
				return err
			}

			if mcpFlag {
				// MCP mode doesn't require git repository - set current directory as root
				currentDir, err := filepath.Abs(".")
				if err != nil {
					return fmt.Errorf("failed to get current directory: %w", err)
				}
				config.SetRootDirectory(currentDir)
				
				// For now, keep the original stdio MCP server approach
				// Agents created will show up in any claude-squad UI via shared storage
				log.Initialize(true) // Enable logging for MCP mode
				fmt.Fprintf(os.Stderr, "🚀 Claude Squad MCP Server (stdio mode)\n")
				fmt.Fprintf(os.Stderr, "   Agents created will appear in claude-squad UI\n")
				fmt.Fprintf(os.Stderr, "   Run 'claude-squad' in another terminal for UI\n\n")
				
				mcpServer := mcp.CreateMCPServer()
				if mcpServer == nil {
					return fmt.Errorf("failed to create MCP server")
				}
				return server.ServeStdio(mcpServer)
			}

			// Check if we're in a git repository (only for non-MCP mode)
			currentDir, err := filepath.Abs(".")
			if err != nil {
				return fmt.Errorf("failed to get current directory: %w", err)
			}

			if !git.IsGitRepo(currentDir) {
				return fmt.Errorf("error: claude-squad must be run from within a git repository")
			}

			// Store the root directory globally for consistent use across UI and MCP
			config.SetRootDirectory(currentDir)

			cfg := config.LoadConfig()

			// Program flag overrides config
			program := cfg.DefaultProgram
			if programFlag != "" {
				program = programFlag
			}
			// AutoYes flag overrides config
			autoYes := cfg.AutoYes
			if autoYesFlag {
				autoYes = true
			}
			if autoYes {
				defer func() {
					if err := daemon.LaunchDaemon(); err != nil {
						log.ErrorLog.Printf("failed to launch daemon: %v", err)
					}
				}()
			}
			// Kill any daemon that's running.
			if err := daemon.StopDaemon(); err != nil {
				log.ErrorLog.Printf("failed to stop daemon: %v", err)
			}

			return app.Run(ctx, program, autoYes)
		},
	}

	resetCmd = &cobra.Command{
		Use:   "reset",
		Short: "Reset all stored instances",
		RunE: func(cmd *cobra.Command, args []string) error {
			log.Initialize(false)
			defer log.Close()

			state := config.LoadState()
			storage, err := session.NewStorage(state)
			if err != nil {
				return fmt.Errorf("failed to initialize storage: %w", err)
			}
			if err := storage.DeleteAllInstances(); err != nil {
				return fmt.Errorf("failed to reset storage: %w", err)
			}
			fmt.Println("Storage has been reset successfully")

			if err := tmux.CleanupSessions(); err != nil {
				return fmt.Errorf("failed to cleanup tmux sessions: %w", err)
			}
			fmt.Println("Tmux sessions have been cleaned up")

			if err := git.CleanupWorktrees(); err != nil {
				return fmt.Errorf("failed to cleanup worktrees: %w", err)
			}
			fmt.Println("Worktrees have been cleaned up")

			// Kill any daemon that's running.
			if err := daemon.StopDaemon(); err != nil {
				return err
			}
			fmt.Println("daemon has been stopped")

			return nil
		},
	}

	debugCmd = &cobra.Command{
		Use:   "debug",
		Short: "Print debug information like config paths",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := config.LoadConfig()

			configDir, err := config.GetConfigDir()
			if err != nil {
				return fmt.Errorf("failed to get config directory: %w", err)
			}
			configJson, _ := json.MarshalIndent(cfg, "", "  ")

			fmt.Printf("Config: %s\n%s\n", filepath.Join(configDir, config.ConfigFileName), configJson)

			return nil
		},
	}

	versionCmd = &cobra.Command{
		Use:   "version",
		Short: "Print the version number of claude-squad",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("claude-squad version %s\n", version)
			fmt.Printf("https://github.com/smtg-ai/claude-squad/releases/tag/v%s\n", version)
		},
	}
)

func init() {
	rootCmd.Flags().StringVarP(&programFlag, "program", "p", "",
		"Program to run in new instances (e.g. 'aider --model ollama_chat/gemma3:1b')")
	rootCmd.Flags().BoolVarP(&autoYesFlag, "autoyes", "y", false,
		"[experimental] If enabled, all instances will automatically accept prompts")
	rootCmd.Flags().BoolVar(&daemonFlag, "daemon", false, "Run a program that loads all sessions"+
		" and runs autoyes mode on them.")
	rootCmd.Flags().BoolVar(&mcpFlag, "mcp", false, "Run as MCP server for Claude Desktop")

	// Hide the daemonFlag and mcpFlag as they're only for internal use
	err := rootCmd.Flags().MarkHidden("daemon")
	if err != nil {
		panic(err)
	}
	err = rootCmd.Flags().MarkHidden("mcp")
	if err != nil {
		panic(err)
	}

	rootCmd.AddCommand(debugCmd)
	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(resetCmd)
}

func main() {
	// Check for explicit --mcp flag first
	for _, arg := range os.Args[1:] {
		if arg == "--mcp" {
			log.Initialize(true) // Initialize logging for MCP server mode
			defer log.Close()      // Ensure log file is closed on exit

			// Run as MCP server using mark3labs/mcp-go
			mcpServer := mcp.CreateMCPServer()
			if mcpServer == nil {
				fmt.Fprintf(os.Stderr, "Failed to create MCP server\n")
				os.Exit(1)
			}
			if err := server.ServeStdio(mcpServer); err != nil {
				fmt.Fprintf(os.Stderr, "MCP server error: %v\n", err)
				os.Exit(1)
			}
			return
		}
	}

	// Normal CLI mode
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
	}
}
