package server

import "strings"

func isGeminiSubscriptionModel(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "subscription/gemini/")
}

// sanitizeGeminiToolDefinitions rewrites OpenAI-style JSON Schema so Gemini's
// GenerateContent proto does not reject empty enum entries or type unions that
// include null. Only the tool parameter schemas are touched.
func sanitizeGeminiToolDefinitions(tools []any) bool {
	changed := false
	for _, item := range tools {
		object, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if schema, ok := toolParameterSchema(object); ok && sanitizeGeminiSchema(schema) {
			changed = true
		}
	}
	return changed
}

func toolParameterSchema(tool map[string]any) (map[string]any, bool) {
	if function, ok := tool["function"].(map[string]any); ok {
		if parameters, ok := function["parameters"].(map[string]any); ok {
			return parameters, true
		}
	}
	if parameters, ok := tool["parameters"].(map[string]any); ok {
		return parameters, true
	}
	if schema, ok := tool["input_schema"].(map[string]any); ok {
		return schema, true
	}
	return nil, false
}

func sanitizeGeminiSchema(node map[string]any) bool {
	if node == nil {
		return false
	}
	changed := walkGeminiSchemaChildren(node)
	if sanitizeGeminiEnum(node) {
		changed = true
	}
	if normalizeGeminiType(node) {
		changed = true
	}
	if flattenGeminiNullUnion(node, "anyOf") {
		changed = true
		if sanitizeGeminiEnum(node) {
			changed = true
		}
		if normalizeGeminiType(node) {
			changed = true
		}
	}
	if flattenGeminiNullUnion(node, "oneOf") {
		changed = true
		if sanitizeGeminiEnum(node) {
			changed = true
		}
		if normalizeGeminiType(node) {
			changed = true
		}
	}
	return changed
}

func walkGeminiSchemaChildren(node map[string]any) bool {
	changed := false
	switch items := node["items"].(type) {
	case map[string]any:
		if sanitizeGeminiSchema(items) {
			changed = true
		}
	case []any:
		if sanitizeGeminiSchemaList(items) {
			changed = true
		}
	}
	for _, key := range []string{"anyOf", "oneOf", "allOf", "prefixItems"} {
		if list, ok := node[key].([]any); ok && sanitizeGeminiSchemaList(list) {
			changed = true
		}
	}
	for _, key := range []string{"not", "if", "then", "else", "contains", "propertyNames", "additionalItems", "unevaluatedItems", "additionalProperties", "unevaluatedProperties"} {
		if child, ok := node[key].(map[string]any); ok && sanitizeGeminiSchema(child) {
			changed = true
		}
	}
	for _, key := range []string{"properties", "patternProperties", "$defs", "definitions"} {
		object, ok := node[key].(map[string]any)
		if !ok {
			continue
		}
		for _, value := range object {
			if child, ok := value.(map[string]any); ok && sanitizeGeminiSchema(child) {
				changed = true
			}
		}
	}
	return changed
}

func sanitizeGeminiSchemaList(list []any) bool {
	changed := false
	for _, item := range list {
		if child, ok := item.(map[string]any); ok && sanitizeGeminiSchema(child) {
			changed = true
		}
	}
	return changed
}

func sanitizeGeminiEnum(node map[string]any) bool {
	raw, ok := node["enum"].([]any)
	if !ok {
		return false
	}
	out := make([]any, 0, len(raw))
	changed := false
	for _, value := range raw {
		if value == nil {
			changed = true
			continue
		}
		if text, ok := value.(string); ok && strings.TrimSpace(text) == "" {
			changed = true
			continue
		}
		out = append(out, value)
	}
	if len(raw) == 0 {
		delete(node, "enum")
		return true
	}
	if !changed {
		return false
	}
	if len(out) == 0 {
		delete(node, "enum")
		return true
	}
	node["enum"] = out
	return true
}

func normalizeGeminiType(node map[string]any) bool {
	switch typed := node["type"].(type) {
	case string:
		if typed != "null" {
			return false
		}
		delete(node, "type")
		node["nullable"] = true
		return true
	case []any:
		nonNull := make([]string, 0, len(typed))
		sawNull := false
		changed := false
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				return false
			}
			if text == "null" {
				sawNull = true
				changed = true
				continue
			}
			if strings.TrimSpace(text) == "" {
				changed = true
				continue
			}
			nonNull = append(nonNull, text)
		}
		if !changed {
			return false
		}
		switch len(nonNull) {
		case 0:
			delete(node, "type")
		case 1:
			node["type"] = nonNull[0]
		default:
			out := make([]any, len(nonNull))
			for i, text := range nonNull {
				out[i] = text
			}
			node["type"] = out
		}
		if sawNull {
			node["nullable"] = true
		}
		return true
	default:
		return false
	}
}

func flattenGeminiNullUnion(node map[string]any, key string) bool {
	branches, ok := node[key].([]any)
	if !ok || len(branches) < 2 {
		return false
	}
	var concrete map[string]any
	nullCount := 0
	for _, branch := range branches {
		object, ok := branch.(map[string]any)
		if !ok {
			return false
		}
		if isNullOnlySchema(object) {
			nullCount++
			continue
		}
		if concrete != nil {
			return false
		}
		concrete = object
	}
	if concrete == nil || nullCount == 0 {
		return false
	}
	for name, value := range concrete {
		if _, exists := node[name]; !exists {
			node[name] = value
		}
	}
	delete(node, key)
	node["nullable"] = true
	return true
}

func isNullOnlySchema(node map[string]any) bool {
	switch typed := node["type"].(type) {
	case string:
		return typed == "null"
	case []any:
		if len(typed) == 0 {
			return false
		}
		for _, item := range typed {
			text, ok := item.(string)
			if !ok || text != "null" {
				return false
			}
		}
		return true
	}
	// normalizeGeminiType rewrites {type:"null"} to {nullable:true}.
	if node["nullable"] != true || node["type"] != nil {
		return false
	}
	for key := range node {
		switch key {
		case "nullable", "description", "title":
			continue
		default:
			return false
		}
	}
	return true
}
