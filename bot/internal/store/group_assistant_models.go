package store

import "time"

// GroupAssistantPolicy is independent from GuardPolicy and moderation records.
type GroupAssistantPolicy struct {
	ChatID            int64
	Version           int64
	ChatEnabled       bool
	LearningEnabled   bool
	TriggerMode       string
	FollowupWindowSec int32
	MaxFollowupTurns  int32
	ChatModelRef      string
	LearningModelRef  string
	Temperature       float64
	SystemPrompt      string
	HistoryLimit      int32
	RetentionDays     int32
	CollectionPolicy  string
	ToolAllowlist     []string
	AllowDomains      []string
	MaxQueueDepth     int32
	MaxQueueWaitSec   int32
	UpdatedBy         *int64
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type UpsertGroupAssistantPolicyParams struct {
	ChatID            int64
	ExpectedVersion   int64
	ChatEnabled       bool
	LearningEnabled   bool
	TriggerMode       string
	FollowupWindowSec int32
	MaxFollowupTurns  int32
	ChatModelRef      string
	LearningModelRef  string
	Temperature       float64
	SystemPrompt      string
	HistoryLimit      int32
	RetentionDays     int32
	CollectionPolicy  string
	ToolAllowlist     []string
	AllowDomains      []string
	MaxQueueDepth     int32
	MaxQueueWaitSec   int32
	UpdatedBy         *int64
}

type GroupAssistantPool struct {
	ChatID    int64
	Version   int64
	Strategy  string
	Config    []byte
	UpdatedBy *int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

type UpsertGroupAssistantPoolParams struct {
	ChatID          int64
	ExpectedVersion int64
	Strategy        string
	Config          []byte
	UpdatedBy       *int64
}

type GroupAssistantMessage struct {
	ID                int64
	ChatID            int64
	ThreadID          int32
	TelegramMessageID int64
	SenderID          int64
	SenderName        string
	Role              string
	Text              string
	Approved          bool
	Delivered         bool
	ContentHash       string
	ExpiresAt         time.Time
	SourceType        string
	SourceID          string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type UpsertGroupAssistantMessageParams struct {
	ChatID            int64
	ThreadID          int32
	TelegramMessageID int64
	SenderID          int64
	SenderName        string
	Role              string
	Text              string
	Approved          bool
	Delivered         bool
	ContentHash       string
	ExpiresAt         time.Time
	SourceType        string
	SourceID          string
}

type GroupAssistantMemory struct {
	ID                 int64
	ChatID             int64
	Subject            string
	Content            string
	MemoryType         string
	AuthorityLevel     string
	ValidScope         string
	SourceType         string
	SourceMessageID    *int64
	SourceChatID       *int64
	SourceOperatorID   *int64
	SourceOperatorName string
	SourceSnippet      string
	SourceCreatedAt    *time.Time
	SourceVerified     string
	SourceContentHash  string
	ExpiresAt          time.Time
	Active             bool
	ForgottenAt        *time.Time
	ForgottenBy        *int64
	DedupeHash         string
	Version            int64
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type CreateGroupAssistantMemoryParams struct {
	ChatID             int64
	Subject            string
	Content            string
	MemoryType         string
	AuthorityLevel     string
	ValidScope         string
	SourceType         string
	SourceMessageID    *int64
	SourceChatID       *int64
	SourceOperatorID   *int64
	SourceOperatorName string
	SourceSnippet      string
	SourceCreatedAt    *time.Time
	SourceVerified     string
	ExpiresAt          time.Time
	DedupeHash         string
	SourceContentHash  string
	ChangedBy          *int64
}

type UpdateGroupAssistantMemoryParams struct {
	ID                 int64
	ChatID             int64
	ExpectedVersion    int64
	Subject            string
	Content            string
	MemoryType         string
	AuthorityLevel     string
	ValidScope         string
	SourceType         string
	SourceMessageID    *int64
	SourceChatID       *int64
	SourceOperatorID   *int64
	SourceOperatorName string
	SourceSnippet      string
	SourceCreatedAt    *time.Time
	SourceVerified     string
	ExpiresAt          time.Time
	DedupeHash         string
	SourceContentHash  string
	ChangedBy          *int64
}

type UpdateGroupAssistantMessageParams struct {
	ChatID            int64
	ThreadID          int32
	TelegramMessageID int64
	SenderID          int64
	SenderName        string
	Text              string
	ContentHash       string
	SourceType        string
	SourceID          string
}

type ResolveGroupAssistantConflictParams struct {
	ID                    int64
	ChatID                int64
	ActorID               int64
	ActorName             string
	ExpectedMemoryVersion int64
	Accept                bool
	ResolutionMode        string
	DedupeHash            string
}

type GroupAssistantMemoryVersion struct {
	ID              int64
	MemoryID        int64
	Version         int64
	Content         string
	MemoryType      string
	AuthorityLevel  string
	ValidScope      string
	SourceType      string
	SourceMessageID *int64
	SourceSnippet   string
	ChangedBy       *int64
	ChangeKind      string
	CreatedAt       time.Time
}

type GroupAssistantConflict struct {
	ID                 int64
	ChatID             int64
	MemoryID           *int64
	Subject            string
	CandidateContent   string
	CandidateScope     string
	CandidateAuthority string
	SourceType         string
	SourceMessageID    *int64
	SourceChatID       *int64
	SourceSnippet      string
	Status             string
	ResolvedBy         *int64
	ResolvedAt         *time.Time
	CreatedAt          time.Time
}

type CreateGroupAssistantConflictParams struct {
	ChatID             int64
	MemoryID           *int64
	Subject            string
	CandidateContent   string
	CandidateScope     string
	CandidateAuthority string
	SourceType         string
	SourceMessageID    *int64
	SourceChatID       *int64
	SourceSnippet      string
}

type GroupAssistantDispatch struct {
	ID         int64
	ChatID     int64
	RequestID  string
	TaskType   string
	EndpointID string
	ModelRef   string
	Reason     string
	Status     string
	ErrorText  string
	LatencyMs  *int32
	CreatedAt  time.Time
}

type CreateGroupAssistantDispatchParams struct {
	ChatID     int64
	RequestID  string
	TaskType   string
	EndpointID string
	ModelRef   string
	Reason     string
	Status     string
	ErrorText  string
	LatencyMs  *int32
}

type ListGroupAssistantMessagesParams struct {
	ChatID   int64
	ThreadID *int32
	SenderID *int64
	Query    string
	Limit    int32
}
