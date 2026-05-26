package bot

import (
	"fmt"
	"strconv"
	"strings"
)

func ParseAllowedChatIDs(value string) (map[int64]struct{}, error) {
	allowed := make(map[int64]struct{})
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		chatID, err := strconv.ParseInt(item, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse Telegram chat ID %q: %w", item, err)
		}
		allowed[chatID] = struct{}{}
	}
	if len(allowed) == 0 {
		return nil, fmt.Errorf("at least one Telegram chat ID is required")
	}
	return allowed, nil
}
