package redact

import (
	"fmt"
	"regexp"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var (
	telegramAPIURLTokenPattern = regexp.MustCompile(`(?i)(https?://api\.telegram\.org/(?:file/)?bot)[0-9]{5,}:[A-Za-z0-9_-]{20,}`)
	telegramBotTokenPattern    = regexp.MustCompile(`(?i)(\bbot)[0-9]{5,}:[A-Za-z0-9_-]{20,}`)
	telegramTokenPattern       = regexp.MustCompile(`\b[0-9]{5,}:[A-Za-z0-9_-]{20,}`)
)

// Text removes Telegram bot tokens from values before they are logged, stored,
// or returned to users.
func Text(value string) string {
	if value == "" {
		return ""
	}
	value = telegramAPIURLTokenPattern.ReplaceAllString(value, `${1}[REDACTED]`)
	value = telegramBotTokenPattern.ReplaceAllString(value, `${1}[REDACTED]`)
	return telegramTokenPattern.ReplaceAllString(value, `[REDACTED]`)
}

func ErrorString(err error) string {
	if err == nil {
		return ""
	}
	return Text(err.Error())
}

func StringPtr(value *string) *string {
	if value == nil {
		return nil
	}
	redacted := Text(*value)
	return &redacted
}

func Error(err error) error {
	if err == nil {
		return nil
	}
	return redactedError{err: err, text: ErrorString(err)}
}

type redactedError struct {
	err  error
	text string
}

func (e redactedError) Error() string {
	return e.text
}

func (e redactedError) Unwrap() error {
	return e.err
}

func ZapLogger(logger *zap.Logger) *zap.Logger {
	if logger == nil {
		return zap.NewNop()
	}
	return logger.WithOptions(zap.WrapCore(func(core zapcore.Core) zapcore.Core {
		return redactingCore{Core: core}
	}))
}

type redactingCore struct {
	zapcore.Core
}

func (c redactingCore) With(fields []zapcore.Field) zapcore.Core {
	return redactingCore{Core: c.Core.With(Fields(fields))}
}

func (c redactingCore) Check(entry zapcore.Entry, checked *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if c.Enabled(entry.Level) {
		return checked.AddCore(entry, c)
	}
	return checked
}

func (c redactingCore) Write(entry zapcore.Entry, fields []zapcore.Field) error {
	entry.Message = Text(entry.Message)
	return c.Core.Write(entry, Fields(fields))
}

func Fields(fields []zapcore.Field) []zapcore.Field {
	if len(fields) == 0 {
		return fields
	}
	out := make([]zapcore.Field, len(fields))
	for i, field := range fields {
		out[i] = Field(field)
	}
	return out
}

func Field(field zapcore.Field) zapcore.Field {
	switch field.Type {
	case zapcore.StringType:
		field.String = Text(field.String)
	case zapcore.ByteStringType:
		field.String = Text(field.String)
		if value, ok := field.Interface.([]byte); ok {
			field.Interface = []byte(Text(string(value)))
		}
	case zapcore.ErrorType:
		if err, ok := field.Interface.(error); ok {
			field.Interface = Error(err)
		}
	case zapcore.StringerType:
		if value, ok := field.Interface.(fmt.Stringer); ok {
			field.Interface = redactedStringer(Text(value.String()))
		}
	case zapcore.ReflectType:
		field.Interface = Value(field.Interface)
	}
	return field
}

func Value(value any) any {
	switch typed := value.(type) {
	case nil:
		return nil
	case string:
		return Text(typed)
	case []byte:
		return []byte(Text(string(typed)))
	case error:
		return Error(typed)
	case fmt.Stringer:
		return redactedStringer(Text(typed.String()))
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = Value(item)
		}
		return out
	case map[string]string:
		out := make(map[string]string, len(typed))
		for key, item := range typed {
			out[key] = Text(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = Value(item)
		}
		return out
	case []string:
		out := make([]string, len(typed))
		for i, item := range typed {
			out[i] = Text(item)
		}
		return out
	default:
		return value
	}
}

type redactedStringer string

func (s redactedStringer) String() string {
	return string(s)
}
