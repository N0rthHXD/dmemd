// Package topic owns the versioned bilingual topic taxonomy. Known aliases are
// canonicalized deterministically; application-specific labels remain extension
// leaves rather than being guessed into an unrelated built-in category.
package topic

import (
	"sort"
	"strings"
)

const Version = "taxonomy-v1"

type Entry struct {
	Name    string
	Parent  string
	Aliases []string
}

var entries = []Entry{
	{"containers", "", []string{"容器", "容器技术", "container"}},
	{"kubernetes", "containers", []string{"k8s", "kube", "库伯内特斯"}},
	{"docker", "containers", []string{"docker容器"}},
	{"oom", "containers", []string{"out of memory", "out-of-memory", "内存耗尽", "节点内存耗尽", "内存溢出", "oomkilled"}},
	{"deployment", "containers", []string{"部署", "容器部署"}},
	{"work", "", []string{"工作", "职业"}},
	{"studio", "work", []string{"工作室", "影棚"}},
	{"course", "work", []string{"课程", "教学"}},
	{"training", "work", []string{"培训", "训练"}},
	{"project", "work", []string{"项目"}},
	{"travel", "", []string{"旅行", "旅游", "出行"}},
	{"hong-kong", "travel", []string{"香港", "hong kong", "hongkong"}},
	{"wuhan", "travel", []string{"武汉"}},
	{"guangzhou", "travel", []string{"广州"}},
	{"itinerary", "travel", []string{"行程", "旅行计划"}},
}

var aliases, parents = buildIndex()

func buildIndex() (map[string]string, map[string]string) {
	a, p := map[string]string{}, map[string]string{}
	for _, entry := range entries {
		a[entry.Name] = entry.Name
		p[entry.Name] = entry.Parent
		for _, alias := range entry.Aliases {
			a[normalize(alias)] = entry.Name
		}
	}
	return a, p
}

func normalize(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(value)), " ")
}

// Canonical includes every ancestor without imposing a cap that could drop a
// parent. Limits belong on raw labels, before hierarchy expansion.
func Canonical(values []string) []string {
	seen := map[string]bool{}
	for _, value := range values {
		value = normalize(value)
		if name, ok := aliases[value]; ok {
			value = name
		}
		for value != "" && !seen[value] {
			seen[value] = true
			value = parents[value]
		}
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

// Raw keeps original spelling and language for audit, trimming only whitespace
// and exact duplicates. It never adds inferred parent labels.
func Raw(values []string) []string {
	result := []string{}
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}
