package model

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// SubscriptionType identifies the model family a paid subscription may use.
// The empty value is intentionally treated as GPT/OpenAI/Codex for backwards
// compatibility with subscriptions created before this field existed.
const (
	SubscriptionTypeGPTOpenAICodex = "gptopenaicodex"
	SubscriptionTypeGrok           = "grok"
	// Short aliases keep integrations readable while the persisted value stays
	// one stable canonical string.
	SubscriptionTypeGPT    = SubscriptionTypeGPTOpenAICodex
	SubscriptionTypeOpenAI = SubscriptionTypeGPTOpenAICodex
	SubscriptionTypeCodex  = SubscriptionTypeGPTOpenAICodex
	SubscriptionTypeXAI    = SubscriptionTypeGrok
)

// SubscriptionModelFamily is the provider family used by subscription access
// checks. Unknown models fail closed for a typed subscription.
type SubscriptionModelFamily string

const (
	SubscriptionModelFamilyUnknown        SubscriptionModelFamily = "unknown"
	SubscriptionModelFamilyGPTOpenAICodex SubscriptionModelFamily = SubscriptionTypeGPTOpenAICodex
	SubscriptionModelFamilyGrok           SubscriptionModelFamily = SubscriptionTypeGrok
)

var ErrSubscriptionModelNotAllowed = errors.New("model is not allowed by the active subscription type")

var subscriptionTablePresence sync.Map // map[*gorm.DB]bool

func hasUserSubscriptionTable(db *gorm.DB) bool {
	if db == nil {
		return false
	}
	if cached, ok := subscriptionTablePresence.Load(db); ok {
		return cached.(bool)
	}
	present := db.Migrator().HasTable(&UserSubscription{})
	subscriptionTablePresence.Store(db, present)
	return present
}

func isMissingSubscriptionTypeColumn(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "subscription_type") &&
		(strings.Contains(message, "no such column") || strings.Contains(message, "unknown column") || strings.Contains(message, "does not exist"))
}

// canonicalSubscriptionType normalizes the small public alias set accepted by
// the admin API. Keeping this list explicit prevents a typo from silently
// widening a subscription.
func canonicalSubscriptionType(value string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.NewReplacer("_", "-", " ", "-").Replace(normalized)
	switch normalized {
	case "", "gpt", "openai", "codex", "gpt-openai-codex", "openai-codex", "gptopenaicodex":
		return SubscriptionTypeGPTOpenAICodex, nil
	case "grok", "xai", "x-ai":
		return SubscriptionTypeGrok, nil
	default:
		return "", fmt.Errorf("invalid subscription type %q (want %s or %s)", value, SubscriptionTypeGPTOpenAICodex, SubscriptionTypeGrok)
	}
}

// NormalizeSubscriptionType provides a compatibility default for legacy rows.
// New writes should call ValidateSubscriptionType first so invalid values are
// rejected rather than silently changed.
func NormalizeSubscriptionType(value string) string {
	typ, err := canonicalSubscriptionType(value)
	if err != nil {
		return SubscriptionTypeGPTOpenAICodex
	}
	return typ
}

func ValidateSubscriptionType(value string) error {
	_, err := canonicalSubscriptionType(value)
	return err
}

// SubscriptionTypeOptions is intentionally stable so clients can render the
// two supported subscription types without duplicating provider policy.
func SubscriptionTypeOptions() []map[string]string {
	return []map[string]string{
		{"value": SubscriptionTypeGPTOpenAICodex, "label": "GPT / OpenAI / Codex only"},
		{"value": SubscriptionTypeGrok, "label": "Grok only"},
	}
}

func normalizeSubscriptionModelName(modelName string) string {
	name := strings.ToLower(strings.TrimSpace(modelName))
	if name == "" {
		return ""
	}
	if index := strings.IndexByte(name, ':'); index >= 0 {
		name = name[:index]
	}
	// Provider-qualified names are accepted (for example xai/grok-4.6). Keep
	// the final component for family matching while checking the provider
	// prefix separately in SubscriptionModelFamilyForModel.
	return name
}

// SubscriptionModelFamilyForModel classifies the public model identifier,
// including provider-qualified and endpoint-suffixed forms.
func SubscriptionModelFamilyForModel(modelName string) SubscriptionModelFamily {
	name := normalizeSubscriptionModelName(modelName)
	if name == "" {
		return SubscriptionModelFamilyUnknown
	}
	if strings.HasPrefix(name, "xai/") || strings.HasPrefix(name, "xai-") || strings.HasPrefix(name, "xai_") ||
		strings.HasPrefix(name, "grok/") || strings.Contains(name, "/grok") {
		return SubscriptionModelFamilyGrok
	}
	base := name
	if index := strings.LastIndexByte(base, '/'); index >= 0 {
		base = base[index+1:]
	}
	if base == "xai" || strings.HasPrefix(base, "grok") {
		return SubscriptionModelFamilyGrok
	}

	openAIPrefixes := []string{
		"gpt-", "gpt_", "codex", "chatgpt", "dall-e", "dalle", "whisper", "tts-",
		"text-embedding", "text-curie", "text-babbage", "text-ada", "text-davinci",
		"omni-moderation", "computer-use", "sora-",
	}
	for _, prefix := range openAIPrefixes {
		if strings.HasPrefix(base, prefix) {
			return SubscriptionModelFamilyGPTOpenAICodex
		}
	}
	if base == "gpt" || base == "openai" {
		return SubscriptionModelFamilyGPTOpenAICodex
	}
	if len(base) >= 2 && base[0] == 'o' && unicode.IsDigit(rune(base[1])) {
		return SubscriptionModelFamilyGPTOpenAICodex
	}
	return SubscriptionModelFamilyUnknown
}

// ModelSubscriptionType is a concise compatibility alias for callers that
// need the canonical two-type value rather than the internal family enum.
func ModelSubscriptionType(modelName string) string {
	switch SubscriptionModelFamilyForModel(modelName) {
	case SubscriptionModelFamilyGPTOpenAICodex:
		return SubscriptionTypeGPTOpenAICodex
	case SubscriptionModelFamilyGrok:
		return SubscriptionTypeGrok
	default:
		return ""
	}
}

func IsModelAllowedBySubscriptionType(subscriptionType string, modelName string) bool {
	typ, err := canonicalSubscriptionType(subscriptionType)
	if err != nil || strings.TrimSpace(modelName) == "" {
		return false
	}
	family := SubscriptionModelFamilyForModel(modelName)
	return (typ == SubscriptionTypeGPTOpenAICodex && family == SubscriptionModelFamilyGPTOpenAICodex) ||
		(typ == SubscriptionTypeGrok && family == SubscriptionModelFamilyGrok)
}

// NormalizeTokenSubscriptionType validates the optional API-key scope. Empty
// means an unscoped legacy key and is intentionally preserved as unrestricted.
func NormalizeTokenSubscriptionType(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", nil
	}
	return canonicalSubscriptionType(value)
}

// TokenSubscriptionTypeAllowsModel applies the API-key scope to one model.
// Unknown model families fail closed for a scoped key.
func TokenSubscriptionTypeAllowsModel(subscriptionType string, modelName string) bool {
	typ, err := NormalizeTokenSubscriptionType(subscriptionType)
	if err != nil {
		return false
	}
	if typ == "" {
		return true
	}
	return IsModelAllowedBySubscriptionType(typ, modelName)
}

func FilterModelsForTokenSubscriptionType(subscriptionType string, modelNames []string) ([]string, error) {
	typ, err := NormalizeTokenSubscriptionType(subscriptionType)
	if err != nil {
		return nil, err
	}
	if typ == "" {
		return modelNames, nil
	}
	filtered := make([]string, 0, len(modelNames))
	for _, modelName := range modelNames {
		if TokenSubscriptionTypeAllowsModel(typ, modelName) {
			filtered = append(filtered, modelName)
		}
	}
	return filtered, nil
}

func (p *SubscriptionPlan) AllowsModel(modelName string) bool {
	if p == nil {
		return false
	}
	return IsModelAllowedBySubscriptionType(p.SubscriptionType, modelName)
}

func (s *UserSubscription) AllowsModel(modelName string) bool {
	if s == nil {
		return false
	}
	return IsModelAllowedBySubscriptionType(s.SubscriptionType, modelName)
}

type SubscriptionAccess struct {
	Enforced bool
	Types    []string
}

func (a SubscriptionAccess) AllowsModel(modelName string) bool {
	if !a.Enforced {
		return true
	}
	for _, typ := range a.Types {
		if IsModelAllowedBySubscriptionType(typ, modelName) {
			return true
		}
	}
	return false
}

func (a SubscriptionAccess) AllowsRequestModel(modelName string, requiresModel bool) bool {
	if !a.Enforced {
		return true
	}
	if strings.TrimSpace(modelName) == "" {
		return !requiresModel
	}
	return a.AllowsModel(modelName)
}

// GetActiveSubscriptionAccess reads the snapshotted type from active rows.
// Rows created before the migration have an empty value and intentionally map
// to GPT/OpenAI/Codex. An invalid non-empty value fails closed.
func GetActiveSubscriptionAccess(userId int) (SubscriptionAccess, error) {
	if userId <= 0 {
		return SubscriptionAccess{}, errors.New("invalid userId")
	}
	if DB == nil {
		return SubscriptionAccess{}, errors.New("database is not initialized")
	}
	// Some deployments/tests intentionally omit the optional subscription
	// tables. Preserve the pre-scope behavior until the normal migration creates
	// them.
	if !hasUserSubscriptionTable(DB) {
		return SubscriptionAccess{Enforced: false, Types: []string{}}, nil
	}
	now := common.GetTimestamp()
	var subs []UserSubscription
	if err := DB.Select("id, plan_id, subscription_type").
		Where("user_id = ? AND status = ? AND end_time > ?", userId, "active", now).
		Find(&subs).Error; err != nil {
		if isMissingSubscriptionTypeColumn(err) {
			return SubscriptionAccess{Enforced: false, Types: []string{}}, nil
		}
		return SubscriptionAccess{}, err
	}
	if len(subs) == 0 {
		return SubscriptionAccess{Enforced: false, Types: []string{}}, nil
	}
	planIDs := make([]int, 0, len(subs))
	seenPlanIDs := make(map[int]struct{}, len(subs))
	for _, sub := range subs {
		if sub.PlanId > 0 {
			if _, ok := seenPlanIDs[sub.PlanId]; !ok {
				seenPlanIDs[sub.PlanId] = struct{}{}
				planIDs = append(planIDs, sub.PlanId)
			}
		}
	}
	planTypes := make(map[int]string, len(planIDs))
	if len(planIDs) > 0 {
		var plans []SubscriptionPlan
		if err := DB.Select("id, subscription_type").Where("id IN ?", planIDs).Find(&plans).Error; err != nil {
			if isMissingSubscriptionTypeColumn(err) {
				plans = nil
			} else {
				return SubscriptionAccess{}, err
			}
		}
		for _, plan := range plans {
			planTypes[plan.Id] = plan.SubscriptionType
		}
	}
	seen := make(map[string]struct{}, len(subs))
	types := make([]string, 0, len(subs))
	for _, sub := range subs {
		typeValue := sub.SubscriptionType
		if strings.TrimSpace(typeValue) == "" {
			typeValue = planTypes[sub.PlanId]
		}
		typ, err := canonicalSubscriptionType(typeValue)
		if err != nil {
			return SubscriptionAccess{}, fmt.Errorf("subscription %d: %w", sub.Id, err)
		}
		if _, ok := seen[typ]; ok {
			continue
		}
		seen[typ] = struct{}{}
		types = append(types, typ)
	}
	sort.Strings(types)
	return SubscriptionAccess{Enforced: true, Types: types}, nil
}

func UserSubscriptionAllowsModel(userId int, modelName string) (bool, error) {
	access, err := GetActiveSubscriptionAccess(userId)
	if err != nil {
		return false, err
	}
	return access.AllowsModel(modelName), nil
}

func FilterModelsForSubscription(userId int, modelNames []string) ([]string, error) {
	access, err := GetActiveSubscriptionAccess(userId)
	if err != nil {
		return nil, err
	}
	if !access.Enforced {
		return modelNames, nil
	}
	filtered := make([]string, 0, len(modelNames))
	for _, modelName := range modelNames {
		if access.AllowsModel(modelName) {
			filtered = append(filtered, modelName)
		}
	}
	return filtered, nil
}

// BackfillSubscriptionTypes is an idempotent migration for rows created
// before subscription_type existed. It only writes empty values and therefore
// preserves any explicit Grok plan/subscription already configured by an
// operator.
func BackfillSubscriptionTypes() error {
	if DB == nil {
		return errors.New("database is not initialized")
	}
	subscriptionTablePresence.Delete(DB)
	return DB.Transaction(func(tx *gorm.DB) error {
		if tx.Migrator().HasTable(&SubscriptionPlan{}) && tx.Migrator().HasColumn(&SubscriptionPlan{}, "subscription_type") {
			var plans []SubscriptionPlan
			if err := tx.Where("subscription_type = '' OR subscription_type IS NULL").Find(&plans).Error; err != nil {
				return err
			}
			for _, plan := range plans {
				if err := tx.Model(&SubscriptionPlan{}).Where("id = ?", plan.Id).
					Update("subscription_type", SubscriptionTypeGPTOpenAICodex).Error; err != nil {
					return err
				}
			}
		}

		if tx.Migrator().HasTable(&UserSubscription{}) && tx.Migrator().HasColumn(&UserSubscription{}, "subscription_type") {
			var subs []UserSubscription
			if err := tx.Where("subscription_type = '' OR subscription_type IS NULL").Find(&subs).Error; err != nil {
				return err
			}
			for _, sub := range subs {
				typ := SubscriptionTypeGPTOpenAICodex
				var plan SubscriptionPlan
				if err := tx.Where("id = ?", sub.PlanId).First(&plan).Error; err == nil {
					typ = NormalizeSubscriptionType(plan.SubscriptionType)
				}
				if err := tx.Model(&UserSubscription{}).Where("id = ?", sub.Id).
					Update("subscription_type", typ).Error; err != nil {
					return err
				}
			}
		}
		// Fail closed legacy ordinary keys after subscription types are known.
		// A key with two possible families cannot be assigned safely, so disable
		// it and leave an explicit operator/user repair marker.
		if tx.Migrator().HasTable(&Token{}) && tx.Migrator().HasColumn(&Token{}, "subscription_type") &&
			tx.Migrator().HasColumn(&Token{}, "scope_assignment_required") {
			var tokens []Token
			if err := tx.Where("(subscription_type = '' OR subscription_type IS NULL) AND scope_exempt = ?", false).Find(&tokens).Error; err != nil {
				return err
			}
			for _, token := range tokens {
				var user User
				if err := tx.Select("id, role").Where("id = ?", token.UserId).First(&user).Error; err == nil && IsAdmin(user.Id) {
					continue
				}
				var subs []UserSubscription
				if err := tx.Select("subscription_type").Where("user_id = ? AND status = ? AND end_time > ?", token.UserId, "active", common.GetTimestamp()).Find(&subs).Error; err != nil {
					return err
				}
				families := map[string]struct{}{}
				for _, sub := range subs {
					typ, err := canonicalSubscriptionType(sub.SubscriptionType)
					if err == nil {
						families[typ] = struct{}{}
					}
				}
				if len(families) == 1 {
					for typ := range families {
						if err := tx.Model(&Token{}).Where("id = ?", token.Id).Updates(map[string]any{"subscription_type": typ, "scope_assignment_required": false}).Error; err != nil {
							return err
						}
					}
				} else {
					if err := tx.Model(&Token{}).Where("id = ?", token.Id).Updates(map[string]any{"status": common.TokenStatusDisabled, "scope_assignment_required": true}).Error; err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
}

// TokenScopeMigrationReport is a secret-free preview of the legacy-key
// migration. Counts are deliberately grouped by action, not by token value.
type TokenScopeMigrationReport struct {
	ExplicitGPT       int `json:"explicit_gpt"`
	ExplicitGrok      int `json:"explicit_grok"`
	EmptySingleFamily int `json:"empty_single_family"`
	EmptyAmbiguous    int `json:"empty_ambiguous"`
	EmptyNoFamily     int `json:"empty_no_family"`
	SystemExempt      int `json:"system_exempt"`
}

// PreviewTokenScopeMigration performs the migration classification without
// writing rows or exposing token secrets. It is suitable for an operator
// dry-run before invoking BackfillSubscriptionTypes.
func PreviewTokenScopeMigration() (TokenScopeMigrationReport, error) {
	var report TokenScopeMigrationReport
	if DB == nil {
		return report, errors.New("database is not initialized")
	}
	var tokens []Token
	if err := DB.Select("id, user_id, subscription_type, scope_exempt").Find(&tokens).Error; err != nil {
		return report, err
	}
	now := common.GetTimestamp()
	for _, token := range tokens {
		if token.ScopeExempt {
			report.SystemExempt++
			continue
		}
		typ := strings.TrimSpace(token.SubscriptionType)
		if typ != "" {
			normalized, err := canonicalSubscriptionType(typ)
			if err == nil && normalized == SubscriptionTypeGrok {
				report.ExplicitGrok++
			} else {
				report.ExplicitGPT++
			}
			continue
		}
		var subs []UserSubscription
		if err := DB.Select("subscription_type").Where("user_id = ? AND status = ? AND end_time > ?", token.UserId, "active", now).Find(&subs).Error; err != nil {
			return report, err
		}
		families := map[string]struct{}{}
		for _, sub := range subs {
			if normalized, err := canonicalSubscriptionType(sub.SubscriptionType); err == nil {
				families[normalized] = struct{}{}
			}
		}
		switch len(families) {
		case 1:
			report.EmptySingleFamily++
		case 0:
			report.EmptyNoFamily++
		default:
			report.EmptyAmbiguous++
		}
	}
	return report, nil
}
