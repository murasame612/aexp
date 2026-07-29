package printer

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/ziwu/aexp/internal/store"
)

const (
	receiptParamKeyWidth   = 14
	receiptParamValueWidth = 17
	maxReceiptParams       = 12
	maxReceiptValueLines   = 3
	maxReceiptCommandLines = 8
)

type receiptParam struct {
	key   string
	value string
	order int
}

func buildStartDetailLines(run *store.Run) []string {
	tokens := receiptCommandTokens(run)
	params := receiptParameters(run, tokens)
	lines := []string{strings.Repeat("-", receiptWidth)}
	if len(params) > 0 {
		lines = append(lines,
			"HYPERPARAMETERS",
			fmt.Sprintf("%-*s %-*s", receiptParamKeyWidth, "HYPERPARAM", receiptParamValueWidth, "VALUE"),
			strings.Repeat("-", receiptParamKeyWidth)+" "+strings.Repeat("-", receiptParamValueWidth),
		)
		shown := params
		if len(shown) > maxReceiptParams {
			shown = shown[:maxReceiptParams]
		}
		for _, param := range shown {
			valueLines := wrapReceiptText(param.value, receiptParamValueWidth)
			if len(valueLines) == 0 {
				valueLines = []string{"-"}
			}
			if len(valueLines) > maxReceiptValueLines {
				valueLines = valueLines[:maxReceiptValueLines]
				valueLines[maxReceiptValueLines-1] = truncateReceiptText(valueLines[maxReceiptValueLines-1]+"...", receiptParamValueWidth)
			}
			key := truncateReceiptText(param.key, receiptParamKeyWidth)
			for index, value := range valueLines {
				if index == 0 {
					lines = append(lines, fmt.Sprintf("%-*s %-*s", receiptParamKeyWidth, key, receiptParamValueWidth, value))
				} else {
					lines = append(lines, fmt.Sprintf("%-*s %-*s", receiptParamKeyWidth, "", receiptParamValueWidth, value))
				}
			}
		}
		if omitted := len(params) - len(shown); omitted > 0 {
			lines = append(lines, fmt.Sprintf("+%d MORE - SEE RUN DETAIL", omitted))
		}
		lines = append(lines, strings.Repeat("-", receiptWidth))
	}

	lines = append(lines, "COMMAND")
	command := formatReceiptCommand(tokens)
	if command == "" {
		command = "(not recorded)"
	}
	commandLines := wrapReceiptText(command, receiptWidth)
	if len(commandLines) > maxReceiptCommandLines {
		commandLines = commandLines[:maxReceiptCommandLines]
		commandLines = append(commandLines, "[TRUNCATED - SEE RUN DETAIL]")
	}
	return append(lines, commandLines...)
}

func receiptCommandTokens(run *store.Run) []string {
	if strings.TrimSpace(run.Program) != "" {
		var args []string
		if err := json.Unmarshal([]byte(run.ArgsJSON), &args); err == nil {
			return append([]string{strings.TrimSpace(run.Program)}, args...)
		}
	}
	return shellWords(run.Command)
}

func receiptParameters(run *store.Run, tokens []string) []receiptParam {
	params := extractCommandParams(tokens)
	seeds := formatSeeds(run.SeedsJSON)
	datasets := formatDatasets(run.DatasetsJSON)
	filtered := params[:0]
	for _, param := range params {
		key := normalizeParamKey(param.key)
		if ((key == "seed" || key == "seeds") && seeds != "") ||
			((key == "dataset" || key == "datasets") && datasets != "") {
			continue
		}
		filtered = append(filtered, param)
	}
	params = filtered

	provenance := make([]receiptParam, 0, 2)
	if seeds != "" {
		provenance = append(provenance, receiptParam{key: "seeds", value: seeds, order: -2})
	}
	if datasets != "" {
		provenance = append(provenance, receiptParam{key: "datasets", value: datasets, order: -1})
	}
	params = append(provenance, params...)
	sort.SliceStable(params, func(i, j int) bool {
		left, right := receiptParamPriority(params[i].key), receiptParamPriority(params[j].key)
		if left != right {
			return left < right
		}
		return params[i].order < params[j].order
	})
	return params
}

func extractCommandParams(tokens []string) []receiptParam {
	params := make([]receiptParam, 0)
	positions := make(map[string]int)
	add := func(key, value string, order int) {
		key = normalizeParamKey(key)
		if key == "" {
			return
		}
		value = strings.TrimSpace(value)
		if isSensitiveParam(key) {
			value = "<redacted>"
		}
		if value == "" {
			value = "true"
		}
		if index, ok := positions[key]; ok {
			params[index].value = value
			return
		}
		positions[key] = len(params)
		params = append(params, receiptParam{key: key, value: value, order: order})
	}

	for index := 0; index < len(tokens); index++ {
		token := strings.TrimSpace(tokens[index])
		if token == "" || isShellOperator(token) {
			continue
		}
		if key, value, ok := splitAssignment(token); ok {
			add(key, value, index)
			continue
		}
		if !strings.HasPrefix(token, "--") || token == "--" {
			continue
		}
		flag := strings.TrimPrefix(token, "--")
		if key, value, ok := strings.Cut(flag, "="); ok {
			add(key, value, index)
			continue
		}
		value := "true"
		if index+1 < len(tokens) && isCommandValue(tokens[index+1]) {
			value = tokens[index+1]
			index++
		}
		add(flag, value, index)
	}
	return params
}

func formatSeeds(raw string) string {
	var seeds []int64
	if err := json.Unmarshal([]byte(raw), &seeds); err != nil || len(seeds) == 0 {
		return ""
	}
	parts := make([]string, 0, len(seeds))
	for _, seed := range seeds {
		parts = append(parts, strconv.FormatInt(seed, 10))
	}
	return strings.Join(parts, ",")
}

func formatDatasets(raw string) string {
	var datasets []store.RunDatasetInput
	if err := json.Unmarshal([]byte(raw), &datasets); err != nil || len(datasets) == 0 {
		return ""
	}
	parts := make([]string, 0, len(datasets))
	for _, dataset := range datasets {
		name := strings.TrimSpace(dataset.DatasetID)
		if name != "" && dataset.Version != "" {
			name += "@" + dataset.Version
		}
		if name == "" {
			name = strings.TrimSpace(dataset.ID)
		}
		if name != "" {
			parts = append(parts, name)
		}
	}
	return strings.Join(parts, ",")
}

func formatReceiptCommand(tokens []string) string {
	redacted := make([]string, 0, len(tokens))
	for index := 0; index < len(tokens); index++ {
		token := tokens[index]
		if key, _, ok := splitAssignment(token); ok && isSensitiveParam(key) {
			redacted = append(redacted, key+"=<redacted>")
			continue
		}
		if strings.HasPrefix(token, "--") {
			flag := strings.TrimPrefix(token, "--")
			if key, _, ok := strings.Cut(flag, "="); ok && isSensitiveParam(key) {
				redacted = append(redacted, "--"+key+"=<redacted>")
				continue
			}
			if isSensitiveParam(flag) {
				redacted = append(redacted, token)
				if index+1 < len(tokens) && isCommandValue(tokens[index+1]) {
					redacted = append(redacted, "<redacted>")
					index++
				}
				continue
			}
		}
		redacted = append(redacted, quoteReceiptToken(token))
	}
	return cleanReceiptText(strings.Join(redacted, " "))
}

func quoteReceiptToken(token string) string {
	if strings.IndexFunc(token, unicode.IsSpace) < 0 {
		return token
	}
	return strconv.Quote(token)
}

func splitAssignment(token string) (string, string, bool) {
	key, value, ok := strings.Cut(token, "=")
	key = strings.TrimSpace(key)
	if !ok || !validParamName(key) {
		return "", "", false
	}
	return key, strings.TrimSuffix(value, ";"), true
}

func validParamName(key string) bool {
	if key == "" {
		return false
	}
	for index, r := range key {
		if index == 0 && !(unicode.IsLetter(r) || r == '_') {
			return false
		}
		if index > 0 && !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_') {
			return false
		}
	}
	return true
}

func normalizeParamKey(key string) string {
	key = strings.TrimSpace(strings.TrimLeft(key, "-"))
	key = strings.ReplaceAll(key, "-", "_")
	if !validParamName(key) {
		return ""
	}
	return strings.ToLower(key)
}

func isSensitiveParam(key string) bool {
	key = normalizeParamKey(key)
	for _, marker := range []string{"password", "passwd", "token", "secret", "api_key", "apikey", "credential", "private_key", "authorization"} {
		if strings.Contains(key, marker) {
			return true
		}
	}
	return false
}

func receiptParamPriority(key string) int {
	key = normalizeParamKey(key)
	if key == "seed" || key == "seeds" || key == "dataset" || key == "datasets" {
		return 0
	}
	for _, marker := range []string{"epoch", "batch", "learning_rate", "lr", "weight_decay", "dropout", "patience", "model", "loss", "hidden", "d_model", "d_ff", "head", "layer", "seq_len", "pred_len"} {
		if strings.Contains(key, marker) {
			return 1
		}
	}
	for _, marker := range []string{"path", "dir", "root", "output", "log", "checkpoint"} {
		if strings.Contains(key, marker) {
			return 3
		}
	}
	return 2
}

func isCommandValue(token string) bool {
	token = strings.TrimSpace(token)
	if token == "" || isShellOperator(token) || strings.HasPrefix(token, "--") {
		return false
	}
	return true
}

func isShellOperator(token string) bool {
	switch token {
	case ";", "|", "||", "&", "&&":
		return true
	default:
		return false
	}
}

func shellWords(command string) []string {
	words := make([]string, 0)
	var current strings.Builder
	var quote rune
	escaped := false
	flush := func() {
		if current.Len() > 0 {
			words = append(words, current.String())
			current.Reset()
		}
	}
	for _, r := range command {
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				current.WriteRune(r)
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			continue
		}
		if unicode.IsSpace(r) {
			flush()
			continue
		}
		if strings.ContainsRune(";|&", r) {
			flush()
			operator := string(r)
			if len(words) > 0 && words[len(words)-1] == operator && (r == '|' || r == '&') {
				words[len(words)-1] += operator
			} else {
				words = append(words, operator)
			}
			continue
		}
		current.WriteRune(r)
	}
	if escaped {
		current.WriteRune('\\')
	}
	flush()
	return words
}

func wrapReceiptText(value string, width int) []string {
	value = cleanReceiptText(value)
	if value == "" || width <= 0 {
		return nil
	}
	runes := []rune(value)
	lines := make([]string, 0, (len(runes)+width-1)/width)
	for len(runes) > width {
		cut := width
		for index := width; index > width/2; index-- {
			if unicode.IsSpace(runes[index-1]) {
				cut = index - 1
				break
			}
		}
		if cut == 0 {
			cut = width
		}
		lines = append(lines, string(runes[:cut]))
		runes = runes[cut:]
		for len(runes) > 0 && unicode.IsSpace(runes[0]) {
			runes = runes[1:]
		}
	}
	if len(runes) > 0 {
		lines = append(lines, string(runes))
	}
	return lines
}
