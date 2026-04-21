package store

import "time"

type Admin struct {
	ID          int64
	TelegramID  int64
	Username    *string
	FirstName   *string
	PhotoURL    *string
	Role        string
	Notes       *string
	GroupScope  []byte
	CreatedAt   time.Time
	LastLoginAt *time.Time
}

type Group struct {
	ID          int64
	ChatID      int64
	Title       string
	Type        string
	MemberCount int32
	Enabled     bool
	JoinedAt    time.Time
	Config      []byte
}

type AuthorizedGroup struct {
	ChatID       int64
	Title        string
	AuthorizedAt time.Time
	AuthorizedBy *int64
	Enabled      bool
	Notes        string
}

type GlobalConfig struct {
	ID        int32
	Config    []byte
	UpdatedAt time.Time
}

type SystemState struct {
	ID                 int32
	AIPaused           bool
	ActionsPaused      bool
	Frozen             bool
	AIPausedReason     string
	AIBudgetLocked     bool
	AIBudgetLockedDate *time.Time
	UpdatedAt          time.Time
	UpdatedBy          *int64
}

type PendingVerification struct {
	ID            int64
	ChatID        int64
	UserID        int64
	Username      *string
	FirstName     *string
	Method        string
	Payload       []byte
	JoinMessageID *int64
	ExpiresAt     time.Time
	CreatedAt     time.Time
}

type Violation struct {
	ID          int64
	ChatID      int64
	UserID      int64
	Username    *string
	Rule        string
	Matched     *string
	Action      string
	MessageText *string
	CreatedAt   time.Time
}

type ProfileCheckLog struct {
	ID           int64
	ChatID       int64
	UserID       int64
	UserName     *string
	Username     *string
	Bio          *string
	CheckMode    string
	Result       string
	MatchedRule  *string
	AiConfidence *float32
	AiVerdict    *string
	CreatedAt    time.Time
}

type Warning struct {
	ID         int64
	ChatID     int64
	UserID     int64
	Reason     *string
	IssuedBy   *int64
	CreatedAt  time.Time
	ConsumedAt *time.Time
}

type ConfigAudit struct {
	ID        int64
	Scope     string
	ChatID    *int64
	AdminID   int64
	Action    string
	Before    []byte
	After     []byte
	Diff      *string
	CreatedAt time.Time
}

type BannedUser struct {
	UserID   int64
	Reason   *string
	Source   string
	BannedAt time.Time
	BannedBy *int64
}

type UserTrust struct {
	ChatID          int64
	UserID          int64
	Username        *string
	FirstName       *string
	LastName        *string
	JoinedAt        time.Time
	UpdatedAt       time.Time
	Status          string
	Score           float64
	MessagesChecked int32
	MessagesClean   int32
	GraduatedAt     *time.Time
	Notes           *string
}

type AIDecision struct {
	ID            int64
	ChatID        int64
	UserID        int64
	MessageID     int64
	MessageText   *string
	ProviderID    *int64
	ModelID       *int64
	Model         string
	PromptVersion string
	Verdict       string
	Confidence    float64
	Category      string
	Reason        *string
	ActionTaken   string
	AdminOverride *string
	LatencyMs     int32
	CostCents     float64
	CreatedAt     time.Time
}

type AICostStat struct {
	Day       time.Time
	Calls     int64
	CostCents float64
	Model     string
}

type AICostDaily struct {
	Date      time.Time
	CostCents float64
	Calls     int64
}

type AICostPerModel struct {
	Model     string
	CostCents float64
	Calls     int64
}

type AICostPerChat struct {
	ChatID    int64
	Title     *string
	CostCents float64
	Calls     int64
}

type LlmProvider struct {
	ID           int64
	Key          string
	Label        string
	Type         string
	BaseURL      string
	ApiKeyEnc    string
	TimeoutMs    int32
	ExtraHeaders []byte
	Enabled      bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type LlmModel struct {
	ID                   int64
	ProviderID           int64
	ModelKey             string
	Label                string
	ApiFormat            string
	Enabled              bool
	SupportsVision       bool
	SupportsJson         bool
	SupportsTools        bool
	CapabilityTags       []string
	Priority             int32
	Meta                 []byte
	ProbeEnabled         bool
	ProbeIntervalSeconds int32
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

type GetModelByRefRow struct {
	ID                   int64
	ProviderID           int64
	ProviderKey          string
	ModelKey             string
	Label                string
	ApiFormat            string
	Enabled              bool
	SupportsVision       bool
	SupportsJson         bool
	SupportsTools        bool
	CapabilityTags       []string
	Priority             int32
	Meta                 []byte
	ProbeEnabled         bool
	ProbeIntervalSeconds int32
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

type GetEnabledModelsWithProviderRow struct {
	ID                   int64
	ProviderID           int64
	ProviderKey          string
	ProviderLabel        string
	ProviderType         string
	BaseURL              string
	ApiKeyEnc            string
	TimeoutMs            int32
	ExtraHeaders         []byte
	ProviderEnabled      bool
	ModelKey             string
	Label                string
	ApiFormat            string
	Enabled              bool
	SupportsVision       bool
	SupportsJson         bool
	SupportsTools        bool
	CapabilityTags       []string
	Priority             int32
	Meta                 []byte
	ProbeEnabled         bool
	ProbeIntervalSeconds int32
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

type ChatTodayStats struct {
	JoinedCount             int64
	VerificationPassedCount int64
	VerificationFailedCount int64
	AICalls                 int64
	AIAdCount               int64
	AIScamCount             int64
	AICleanCount            int64
	BanKickCount            int64
	WarnCount               int64
	AICostCents             float64
}

type ResolvedChatUser struct {
	UserID   int64
	Username *string
}

type LlmModelStat struct {
	ModelID      int64
	LastCheckAt  *time.Time
	LastOkAt     *time.Time
	LastError    string
	Healthy      bool
	LatencyP50Ms *int32
	LatencyP95Ms *int32
	Success1h    int32
	Fail1h       int32
	Success24h   int32
	Fail24h      int32
	UpdatedAt    time.Time
}
