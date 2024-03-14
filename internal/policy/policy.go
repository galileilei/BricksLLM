package policy

import (
	"errors"
	"net/http"
	"regexp"
	"strings"

	internal_errors "github.com/bricks-cloud/bricksllm/internal/errors"
	"github.com/bricks-cloud/bricksllm/internal/pii"
	"github.com/bricks-cloud/bricksllm/internal/stats"

	goopenai "github.com/sashabaranov/go-openai"
)

type Action string

const (
	Block          Action = "block"
	AllowButWarn   Action = "allow_but_warn"
	AllowButRedact Action = "allow_but_redact"
	Allow          Action = "allow"
)

type Rule string

const (
	Address                             Rule = "address"
	Age                                 Rule = "age"
	All                                 Rule = "all"
	AwsAccessKey                        Rule = "aws_access_key"
	AwsSecretKey                        Rule = "aws_secret_key"
	BankAccountNumber                   Rule = "bank_account_number"
	BankRouting                         Rule = "bank_routing"
	CaHealthNumber                      Rule = "ca_health_number"
	CaSocialInsuranceNumber             Rule = "ca_social_insurance_number"
	CreditDebitCvv                      Rule = "credit_debit_cvv"
	CreditDebitExpiry                   Rule = "credit_debit_expiry"
	CreditDebitNumber                   Rule = "credit_debit_number"
	DateTime                            Rule = "date_time"
	DriverId                            Rule = "driver_id"
	Email                               Rule = "email"
	InAadhaar                           Rule = "in_aadhaar"
	InNrega                             Rule = "in_nrega"
	InPermanentAccountNumber            Rule = "in_permanent_account_number"
	InVoterNumber                       Rule = "in_voter_number"
	InternationalBankAccountNumber      Rule = "international_bank_account_number"
	IpAddress                           Rule = "ip_address"
	LicensePlate                        Rule = "license_plate"
	MacAddress                          Rule = "mac_address"
	Name                                Rule = "name"
	PassportNumber                      Rule = "passport_number"
	Password                            Rule = "password"
	Phone                               Rule = "phone"
	Pin                                 Rule = "pin"
	Ssn                                 Rule = "ssn"
	SwiftCode                           Rule = "swift_code"
	UkNationalHealthServiceNumber       Rule = "uk_national_health_service_number"
	UkNationalInsuranceNumber           Rule = "uk_national_insurance_number"
	UkUniqueTaxpayerReferenceNumber     Rule = "uk_unique_taxpayer_reference_number"
	Url                                 Rule = "url"
	UsIndividualTaxIdentificationNumber Rule = "us_individual_tax_identification_number"
	Username                            Rule = "username"
	VehicleIdentificationNumber         Rule = "vehicle_identification_number"
)

type CustomRule struct {
	Definition string `json:"definition"`
	Action     Action `json:"action"`
}

type RegularExpressionRule struct {
	Definition string `json:"definition"`
	Action     Action `json:"action"`
}

type Config struct {
	Rules map[Rule]Action `json:"rules"`
}

type RegexConfig struct {
	RegularExpressionRules []*RegularExpressionRule `json:"rules"`
}

type CustomConfig struct {
	CustomRules []*CustomRule `json:"rules"`
}

type Policy struct {
	Id           string        `json:"id"`
	Name         string        `json:"name"`
	CreatedAt    int64         `json:"createdAt"`
	UpdatedAt    int64         `json:"updatedAt"`
	Tags         []string      `json:"tags"`
	Config       *Config       `json:"config"`
	RegexConfig  *RegexConfig  `json:"regexConfig"`
	CustomConfig *CustomConfig `json:"customConfig"`
}

type UpdatePolicy struct {
	Name         string        `json:"name"`
	UpdatedAt    int64         `json:"updatedAt"`
	Tags         []string      `json:"tags"`
	Config       *Config       `json:"config"`
	RegexConfig  *RegexConfig  `json:"regexConfig"`
	CustomConfig *CustomConfig `json:"customConfig"`
}

type Request struct {
	Contents []string `json:"contents"`
	Policy   *Policy  `json:"policy"`
}

type Response struct {
	Contents       []string        `json:"contents"`
	Action         Action          `json:"action"`
	Warnings       map[string]bool `json:"warnings"`
	BlockedReasons map[string]bool `json:"blockedReasons"`
}

func (p *Policy) Filter(client http.Client, input any, scanner Scanner) error {
	if p == nil || scanner == nil {
		return nil
	}

	shouldInspect := false
	if p.Config != nil {
		for _, action := range p.Config.Rules {
			if action != Allow {
				shouldInspect = true
			}
		}
	}

	if p.RegexConfig != nil {
		for _, regexr := range p.RegexConfig.RegularExpressionRules {
			if regexr.Action != Allow {
				shouldInspect = true
			}
		}
	}

	if p.CustomConfig != nil {
		for _, cr := range p.CustomConfig.CustomRules {
			if cr.Action != Allow {
				shouldInspect = true
			}
		}
	}

	if !shouldInspect {
		return nil
	}

	switch input.(type) {
	case *goopenai.EmbeddingRequest:
		converted := input.(*goopenai.EmbeddingRequest)
		if inputs, ok := converted.Input.([]interface{}); ok {
			inputsToInspect := []string{}

			for _, input := range inputs {
				stringified, ok := input.(string)
				if !ok {
					return errors.New("input is not string")
				}

				inputsToInspect = append(inputsToInspect, stringified)
			}

			result, err := p.scan(inputsToInspect, scanner)
			if err != nil {
				return err
			}

			if result.Action == Block {
				return internal_errors.NewBlockedError("request blocked due to detected entities: " + join(result.BlockedEntities))
			}

			if result.Action == AllowButWarn {
				return internal_errors.NewBlockedError("request warned due to detected entities: " + join(result.WarnedEntities))
			}

			if len(result.Updated) == 1 {
				converted.Input = result.Updated[0]
			}
		} else if input, ok := converted.Input.(string); ok {
			result, err := p.scan([]string{input}, scanner)
			if err != nil {
				return err
			}

			if result.Action == Block {
				return internal_errors.NewBlockedError("request blocked due to detected entities: " + join(result.BlockedEntities))
			}

			if result.Action == AllowButWarn {
				return internal_errors.NewBlockedError("request warned due to detected entities: " + join(result.WarnedEntities))
			}

			if len(result.Updated) == 1 {
				converted.Input = result.Updated[0]
			}
		}

		return nil
	case *goopenai.ChatCompletionRequest:
		converted := input.(*goopenai.ChatCompletionRequest)
		newMessages := []goopenai.ChatCompletionMessage{}

		contents := []string{}
		for _, message := range converted.Messages {
			contents = append(contents, message.Content)
		}

		result, err := p.scan(contents, scanner)

		if err != nil {
			return err
		}

		if result.Action == Block {
			return internal_errors.NewBlockedError("request blocked due to detected entities: " + join(result.BlockedEntities))
		}

		if result.Action == AllowButWarn {
			return internal_errors.NewBlockedError("request warned due to detected entities: " + join(result.WarnedEntities))
		}

		if len(result.Updated) != len(converted.Messages) {
			return errors.New("updated contents length not consistent with existing content length")
		}

		for index, c := range result.Updated {
			newMessages = append(newMessages, goopenai.ChatCompletionMessage{
				Content:      c,
				Role:         converted.Messages[index].Role,
				ToolCalls:    converted.Messages[index].ToolCalls,
				ToolCallID:   converted.Messages[index].ToolCallID,
				Name:         converted.Messages[index].Name,
				FunctionCall: converted.Messages[index].FunctionCall,
			})
		}

		converted.Messages = newMessages

		return nil
	}

	return nil
}

func join(entities []Rule) string {
	strs := []string{}
	for _, entity := range entities {
		strs = append(strs, string(entity))
	}

	return strings.Join(strs, " ,")
}

type Scanner interface {
	Scan(input []string) (*pii.Result, error)
}

var entityMap map[string]string = map[string]string{
	"BANK_ACCOUNT_NUMBER":           "bank_account_number",
	"BANK_ROUTING":                  "bank_routing",
	"CREDIT_DEBIT_NUMBER":           "credit_debit_number",
	"CREDIT_DEBIT_CVV":              "credit_debit_cvv",
	"CREDIT_DEBIT_EXPIRY":           "credit_debit_expiry",
	"PIN":                           "pin",
	"EMAIL":                         "email",
	"ADDRESS":                       "address",
	"NAME":                          "name",
	"PHONE":                         "phone",
	"SSN":                           "ssn",
	"DATE_TIME":                     "date_time",
	"PASSPORT_NUMBER":               "passport_number",
	"DRIVER_ID":                     "driver_id",
	"URL":                           "url",
	"AGE":                           "age",
	"USERNAME":                      "username",
	"PASSWORD":                      "password",
	"AWS_ACCESS_KEY":                "aws_access_key",
	"AWS_SECRET_KEY":                "aws_secret_key",
	"IP_ADDRESS":                    "ip_address",
	"MAC_ADDRESS":                   "mac_address",
	"ALL":                           "all",
	"LICENSE_PLATE":                 "license_plate",
	"VEHICLE_IDENTIFICATION_NUMBER": "vehicle_identification_number",
	"UK_NATIONAL_INSURANCE_NUMBER":  "uk_national_insurance_number",
	"CA_SOCIAL_INSURANCE_NUMBER":    "ca_social_insurance_number",
	"US_INDIVIDUAL_TAX_IDENTIFICATION_NUMBER": "us_individual_tax_identification_number",
	"UK_UNIQUE_TAXPAYER_REFERENCE_NUMBER":     "uk_unique_taxpayer_reference_number",
	"IN_PERMANENT_ACCOUNT_NUMBER":             "in_permanent_account_number",
	"IN_NREGA":                                "in_nrega",
	"INTERNATIONAL_BANK_ACCOUNT_NUMBER":       "international_bank_account_number",
	"SWIFT_CODE":                              "swift_code",
	"UK_NATIONAL_HEALTH_SERVICE_NUMBER":       "uk_national_health_service_number",
	"CA_HEALTH_NUMBER":                        "ca_health_number",
	"IN_AADHAAR":                              "in_aadhaar",
	"IN_VOTER_NUMBER":                         "in_voter_number",
}

type ScanResult struct {
	Action                  Action
	BlockedEntities         []Rule
	WarnedEntities          []Rule
	BlockedRegexDefinitions []string
	WarnedRegexDefinitions  []string
	Updated                 []string
}

func (p *Policy) scan(input []string, scanner Scanner) (*ScanResult, error) {
	sr := &ScanResult{
		Action: Allow,
	}

	if p.Config != nil {
		r, err := scanner.Scan(input)
		if err != nil {
			return nil, err
		}

		found := map[string]bool{}
		for _, detection := range r.Detections {
			for _, entity := range detection.Entities {
				converted, ok := entityMap[entity.Type]
				if !ok {
					continue
				}

				found[converted] = true
			}
		}

		blockedEntities := []Rule{}
		warnedEntities := []Rule{}
		redactedEntities := map[Rule]bool{}

		if p.Config != nil {
			for rule, action := range p.Config.Rules {
				_, ok := found[string(rule)]
				if action == Block && ok {
					blockedEntities = append(blockedEntities, rule)
				} else if action == AllowButWarn && ok {
					warnedEntities = append(warnedEntities, rule)
				} else if action == AllowButRedact && ok {
					redactedEntities[rule] = true
				}
			}
		}

		if len(blockedEntities) != 0 {
			sr.Action = Block
			sr.BlockedEntities = blockedEntities
		}

		if len(warnedEntities) != 0 {
			if sr.Action != Block {
				sr.Action = AllowButWarn
			}

			sr.WarnedEntities = warnedEntities
		}

		for _, detection := range r.Detections {
			replaced := detection.Input

			for _, entity := range detection.Entities {
				converted, ok := entityMap[entity.Type]
				if !ok {
					continue
				}

				_, ok = redactedEntities[Rule(converted)]
				if ok {
					if sr.Action != Block && sr.Action != AllowButWarn {
						sr.Action = AllowButRedact
					}
					old := replaced[entity.BeginOffset:entity.EndOffset]
					replaced = strings.ReplaceAll(replaced, old, "***")
				}
			}

			sr.Updated = append(sr.Updated, replaced)
		}
	}

	if p.RegexConfig != nil {
		found := map[string]bool{}
		for _, text := range sr.Updated {
			for _, rule := range p.RegexConfig.RegularExpressionRules {
				regex, err := regexp.Compile(rule.Definition)
				if err != nil {
					stats.Incr("bricksllm.policy.scanner.scan.regex_compile_error", nil, 1)
					continue
				}

				match := regex.FindString(text)
				if len(match) != 0 {
					found[rule.Definition] = true
				}
			}
		}

		blockedRegexDefinitions := []string{}
		warnedRegexDefinitions := []string{}

		for _, rule := range p.RegexConfig.RegularExpressionRules {
			_, ok := found[rule.Definition]
			if ok && rule.Action == Block {
				blockedRegexDefinitions = append(blockedRegexDefinitions, rule.Definition)
			}

			if ok && rule.Action == AllowButWarn {
				warnedRegexDefinitions = append(warnedRegexDefinitions, rule.Definition)
			}
		}

		if len(blockedRegexDefinitions) != 0 {
			sr.Action = Block
			sr.BlockedRegexDefinitions = blockedRegexDefinitions
		}

		if len(warnedRegexDefinitions) != 0 {
			if sr.Action != Block {
				sr.Action = AllowButWarn
			}

			sr.WarnedRegexDefinitions = warnedRegexDefinitions
		}

		updated := []string{}
		for _, text := range sr.Updated {
			replaced := text

			for _, rule := range p.RegexConfig.RegularExpressionRules {
				if rule.Action == AllowButRedact {
					regex, err := regexp.Compile(rule.Definition)
					if err != nil {
						stats.Incr("bricksllm.policy.scanner.scan.regex_compile_error", nil, 1)
						continue
					}

					if sr.Action != Block && sr.Action != AllowButWarn {
						sr.Action = AllowButRedact
					}

					replaced = regex.ReplaceAllString(text, "***")
				}
			}

			updated = append(updated, replaced)
		}

		sr.Updated = updated
	}

	return sr, nil
}

// func (p *Policy) inspect(client http.Client, contents []string) ([]string, error) {
// 	data, err := json.Marshal(&Request{
// 		Contents: contents,
// 		Policy:   p,
// 	})

// 	if err != nil {
// 		return nil, err
// 	}

// 	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
// 	defer cancel()

// 	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost:8080/inspect", io.NopCloser(bytes.NewReader(data)))
// 	if err != nil {
// 		return nil, err
// 	}

// 	req.Header.Add("Content-Type", "application/json")

// 	res, err := client.Do(req)
// 	if err != nil {
// 		return nil, err
// 	}

// 	defer res.Body.Close()

// 	body, err := io.ReadAll(res.Body)
// 	if err != nil {
// 		return nil, err
// 	}

// 	parsed := &Response{}
// 	err = json.Unmarshal(body, &parsed)
// 	if err != nil {
// 		return nil, err
// 	}

// 	blockedReasons := []string{}
// 	for blocked := range parsed.BlockedReasons {
// 		blockedReasons = append(blockedReasons, blocked)
// 	}

// 	warnings := []string{}
// 	for message := range parsed.Warnings {
// 		warnings = append(warnings, message)
// 	}

// 	if parsed.Action == Block {
// 		return nil, internal_errors.NewBlockedError(fmt.Sprintf("request blocked: %s", blockedReasons))
// 	}

// 	if len(parsed.Warnings) != 0 {
// 		return nil, internal_errors.NewWarningError(fmt.Sprintf("request warned: %s", warnings))
// 	}

// 	return parsed.Contents, nil
// }
