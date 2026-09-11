package server

import "strings"

func copyProvenance(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func mergeAuthProvenance(base, auth map[string]string) map[string]string {
	result := copyProvenance(base)
	for key := range result {
		if strings.HasPrefix(key, "auth.") {
			delete(result, key)
		}
	}
	for key, value := range auth {
		if strings.HasPrefix(key, "auth.") {
			result[key] = value
		}
	}
	return result
}
