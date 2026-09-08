package store

import "time"

// GroupAssistantPolicy is independent from GuardPolicy and moderation records.
type GroupAssistantPolicy struct {
	ChatID                    int64
	Version                   int64
	ChatEnabled               bool
	LearningEnabled           bool
	TriggerMode               string
	FollowupWindowSec         int32
	MaxFollowupTurns          int32
	ChatModelRef              string
	LearningModelRef          string
	Temperature               float64
	SystemPrompt              string
	HistoryLimit              int32
	RetentionDays             int32
	CollectionPolicy          string
	ToolAllowlist             []string
	AllowDomains              []string
	MaxQueueDepth             int32
	MaxQueueWaitSec           int32
	ProactiveInterjectEnabled bool
	ProactiveColdTopicEnabled bool
	ColdTopicIdleMinutes      int32
	ColdTopicQuietStart       int32
	ColdTopicQuietEnd         int32
	MimicTargetUserID         int64
	MimicTargetUserName       string
	MimicProfileText          string
	MimicSampleCount          int32
	MimicDistilledAtCount     int32
	TTSMode                   string
	StickerFallbackFileIDs    []string
	ProactiveTaskBrief        string
	UpdatedBy                 *int64
	CreatedAt                 time.Time
	UpdatedAt                 time.Time
}

type UpsertGroupAssistantPolicyParams struct {
	ChatID                    int64
	ExpectedVersion           int64
	ChatEnabled               bool
	LearningEnabled           bool
	TriggerMode               string
	FollowupWindowSec         int32
	MaxFollowupTurns          int32
	ChatModelRef              string
	LearningModelRef          string
	Temperature               float64
	SystemPrompt              string
	HistoryLimit              int32
	RetentionDays             int32
	CollectionPolicy          string
	ToolAllowlist             []string
	AllowDomains              []string
	MaxQueueDepth             int32
	MaxQueueWaitSec           int32
	ProactiveInterjectEnabled bool
	ProactiveColdTopicEnabled bool
	ColdTopicIdleMinutes      int32
	ColdTopicQuietStart       int32
	ColdTopicQuietEnd         int32
	MimicTargetUserID         int64
	MimicTargetUserName       string
	MimicProfileText          string
	MimicSampleCount          int32
	MimicDistilledAtCount     int32
	TTSMode                   string
	StickerFallbackFileIDs    []string
	ProactiveTaskBrief        string
	UpdatedBy                 *int64
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

type GroupAssistantStyleSample struct {
	ID        int64
	ChatID    int64
	UserID    int64
	Content   string
	CreatedAt time.Time
}

type GroupAssistantRecentSender struct {
	SenderID     int64
	SenderName   string
	MessageCount int64
	LastSeenAt   time.Time
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

type CreateGroupAssistantStyleSampleParams struct {
	ChatID  int64
	UserID  int64
	Content string
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

// AssistantModelLoadOptions references existing registry models only.
type AssistantModelLoadOptions struct {
	Weight          int `json:"weight"`
	MaxConcurrency  int `json:"max_concurrency"`
	TimeoutMs       int `json:"timeout_ms"`
	CooldownSeconds int `json:"cooldown_duration_sec"`
}

type AssistantModelRoleConfig struct {
	Strategy     string                               `json:"strategy,omitempty"`
	ModelOptions map[string]AssistantModelLoadOptions `json:"model_options,omitempty"`
	ModelRef     string                               `json:"model_ref"`
	TimeoutSec   float64                              `json:"timeout_sec"`
	Temperature  float64                              `json:"temperature"`
	MaxTokens    int                                  `json:"max_tokens"`
	Fallbacks    []string                             `json:"fallbacks"`
}

type AssistantGlobalSettings struct {
	ID                        int16
	Version                   int64
	ModelRoles                []byte
	InboundMergeWindowSec     float64
	ReplyTotalTimeoutSec      float64
	DecisionContextItems      int32
	KeepOriginalText          bool
	MemoryRecallEnabled       bool
	HotWindowCompressEnabled  bool
	ProactiveIdleMinutes      int32
	ProactiveQuietStart       int32
	ProactiveQuietEnd         int32
	ProactiveCheckIntervalSec float64
	TTSEnabled                bool
	TTSHTTPTimeoutSec         float64
	TTSMaxTextLength          int32
	TTSAPIBase                string
	TTSAppID                  string
	TTSAppKeyEnc              string
	TTSAccessKeyEnc           string
	TTSResourceID             string
	TTSModel                  string
	TTSSpeaker                string
	TTSAudioFormat            string
	TTSSampleRate             int32
	TTSBitRate                int32
	TTSEmotion                string
	TTSEmotionScale           int32
	TTSSpeechRate             int32
	TTSLoudnessRate           int32
	TTSSilenceDurationMS      int32
	StickerFallbackFileIDs    []string
	UpdatedBy                 *int64
	CreatedAt                 time.Time
	UpdatedAt                 time.Time
}

type UpsertAssistantGlobalSettingsParams struct {
	ExpectedVersion           int64
	ModelRoles                []byte
	InboundMergeWindowSec     float64
	ReplyTotalTimeoutSec      float64
	DecisionContextItems      int32
	KeepOriginalText          bool
	MemoryRecallEnabled       bool
	HotWindowCompressEnabled  bool
	ProactiveIdleMinutes      int32
	ProactiveQuietStart       int32
	ProactiveQuietEnd         int32
	ProactiveCheckIntervalSec float64
	TTSEnabled                bool
	TTSHTTPTimeoutSec         float64
	TTSMaxTextLength          int32
	TTSAPIBase                string
	TTSAppID                  string
	TTSAppKeyEnc              string
	TTSAccessKeyEnc           string
	TTSResourceID             string
	TTSModel                  string
	TTSSpeaker                string
	TTSAudioFormat            string
	TTSSampleRate             int32
	TTSBitRate                int32
	TTSEmotion                string
	TTSEmotionScale           int32
	TTSSpeechRate             int32
	TTSLoudnessRate           int32
	TTSSilenceDurationMS      int32
	StickerFallbackFileIDs    []string
	UpdatedBy                 *int64
}

type AssistantPromptOverride struct {
	ChatID    int64
	PromptKey string
	Content   string
	UpdatedAt time.Time
}

type AssistantStickerSample struct {
	ID              int64
	ChatID          int64
	FileID          string
	SourceMessageID *int64
	Query           string
	Emoji           string
	SetName         string
	Aliases         []string
	SeenCount       int32
	SentCount       int32
	Source          string
	CreatedAt       time.Time
	LastSeenAt      *time.Time
	LastSentAt      *time.Time
}

type UpsertAssistantStickerSampleParams struct {
	ChatID          int64
	FileID          string
	SourceMessageID *int64
	Query           string
	Emoji           string
	SetName         string
	Source          string
}
