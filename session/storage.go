package session

import (
	"claude-squad/config"
	"claude-squad/log"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// InstanceData represents the serializable data of an Instance
type InstanceData struct {
	Title     string    `json:"title"`
	Path      string    `json:"path"`
	Branch    string    `json:"branch"`
	Status    Status    `json:"status"`
	Height    int       `json:"height"`
	Width     int       `json:"width"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	AutoYes   bool      `json:"auto_yes"`

	Program   string          `json:"program"`
	Worktree  GitWorktreeData `json:"worktree"`
	DiffStats DiffStatsData   `json:"diff_stats"`
}

// GitWorktreeData represents the serializable data of a GitWorktree
type GitWorktreeData struct {
	RepoPath      string `json:"repo_path"`
	WorktreePath  string `json:"worktree_path"`
	SessionName   string `json:"session_name"`
	BranchName    string `json:"branch_name"`
	BaseCommitSHA string `json:"base_commit_sha"`
}

// DiffStatsData represents the serializable data of a DiffStats
type DiffStatsData struct {
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	Content string `json:"content"`
}

// Storage handles saving and loading instances using the state interface
type Storage struct {
	state config.InstanceStorage
}

// NewStorage creates a new storage instance
func NewStorage(state config.InstanceStorage) (*Storage, error) {
	return &Storage{
		state: state,
	}, nil
}

// SaveInstances saves the list of instances to disk
func (s *Storage) SaveInstances(instances []*Instance) error {
	// Convert instances to InstanceData
	data := make([]InstanceData, 0)
	for _, instance := range instances {
		// Save started instances, or MCP agent instances (which may have timing issues)
		if instance.Started() || strings.HasPrefix(instance.Title, "Agent-agent-") {
			data = append(data, instance.ToInstanceData())
		}
	}

	// Marshal to JSON
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to marshal instances: %w", err)
	}

	return s.state.SaveInstances(jsonData)
}

// LoadInstances loads the list of instances from disk
func (s *Storage) LoadInstances() ([]*Instance, error) {
	jsonData := s.state.GetInstances()

	var instancesData []InstanceData
	if err := json.Unmarshal(jsonData, &instancesData); err != nil {
		return nil, fmt.Errorf("failed to unmarshal instances: %w", err)
	}

	log.InfoLog.Printf("LoadInstances: Unmarshaled %d instances from JSON", len(instancesData))
	for i, data := range instancesData {
		log.InfoLog.Printf("LoadInstances: Instance %d: title='%s', path='%s'", i, data.Title, data.Path)
	}

	instances := make([]*Instance, len(instancesData))
	for i, data := range instancesData {
		log.InfoLog.Printf("LoadInstances: Creating instance from data: %s", data.Title)
		instance, err := FromInstanceData(data)
		if err != nil {
			log.ErrorLog.Printf("LoadInstances: Failed to create instance %s: %v", data.Title, err)
			return nil, fmt.Errorf("failed to create instance %s: %w", data.Title, err)
		}
		instances[i] = instance
		log.InfoLog.Printf("LoadInstances: Successfully created instance: %s", data.Title)
	}

	log.InfoLog.Printf("LoadInstances: Returning %d instances", len(instances))
	return instances, nil
}

// DeleteInstance removes an instance from storage
func (s *Storage) DeleteInstance(title string) error {
	// Always get fresh state to ensure we see latest changes from MCP
	freshState := config.LoadState()
	freshStorage, err := NewStorage(freshState)
	if err != nil {
		return fmt.Errorf("failed to create fresh storage: %w", err)
	}
	
	instances, err := freshStorage.LoadInstances()
	if err != nil {
		return fmt.Errorf("failed to load instances: %w", err)
	}

	found := false
	newInstances := make([]*Instance, 0)
	log.InfoLog.Printf("DELETE: Looking for instance '%s' among %d loaded instances", title, len(instances))
	
	for _, instance := range instances {
		data := instance.ToInstanceData()
		log.InfoLog.Printf("DELETE: Checking instance '%s' against target '%s'", data.Title, title)
		if data.Title != title {
			newInstances = append(newInstances, instance)
		} else {
			found = true
			log.InfoLog.Printf("DELETE: Found instance to delete: '%s'", title)
		}
	}

	if !found {
		log.ErrorLog.Printf("DELETE: Instance not found in storage: '%s'", title)
		return fmt.Errorf("instance not found: %s", title)
	}

	log.InfoLog.Printf("DELETE: Saving %d remaining instances after deleting '%s'", len(newInstances), title)
	return freshStorage.SaveInstances(newInstances)
}

// UpdateInstance updates an existing instance in storage
func (s *Storage) UpdateInstance(instance *Instance) error {
	instances, err := s.LoadInstances()
	if err != nil {
		return fmt.Errorf("failed to load instances: %w", err)
	}

	data := instance.ToInstanceData()
	found := false
	for i, existing := range instances {
		existingData := existing.ToInstanceData()
		if existingData.Title == data.Title {
			instances[i] = instance
			found = true
			break
		}
	}

	if !found {
		return fmt.Errorf("instance not found: %s", data.Title)
	}

	return s.SaveInstances(instances)
}

// DeleteAllInstances removes all stored instances
func (s *Storage) DeleteAllInstances() error {
	return s.state.DeleteAllInstances()
}
