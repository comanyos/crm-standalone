package domain

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

const maxSearchResults = 50

// SearchRecord is deliberately a small navigation result, not a second
// transport representation of every entity. Detail pages remain authoritative.
type SearchRecord struct {
	ObjectType string
	ID         string
	Title      string
	Subtitle   string
}

func validateSearch(query string, limit int) (string, int, error) {
	query = strings.TrimSpace(query)
	if len([]rune(query)) < 2 {
		return "", 0, fmt.Errorf("%w: search query must contain at least two characters", ErrInvalid)
	}
	if len([]rune(query)) > 200 {
		return "", 0, fmt.Errorf("%w: search query exceeds 200 characters", ErrInvalid)
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > maxSearchResults {
		limit = maxSearchResults
	}
	return strings.ToLower(query), limit, nil
}

func searchRank(query string, values ...string) int {
	for _, value := range values {
		if strings.HasPrefix(strings.ToLower(value), query) {
			return 2
		}
	}
	for _, value := range values {
		if strings.Contains(strings.ToLower(value), query) {
			return 1
		}
	}
	return 0
}

func rankSearchResults(query string, results []SearchRecord, limit int) []SearchRecord {
	filtered := results[:0]
	for _, result := range results {
		if searchRank(query, result.Title, result.Subtitle) > 0 {
			filtered = append(filtered, result)
		}
	}
	sort.Slice(filtered, func(i, j int) bool {
		left, right := searchRank(query, filtered[i].Title, filtered[i].Subtitle), searchRank(query, filtered[j].Title, filtered[j].Subtitle)
		if left != right {
			return left > right
		}
		if filtered[i].Title != filtered[j].Title {
			return strings.ToLower(filtered[i].Title) < strings.ToLower(filtered[j].Title)
		}
		return filtered[i].ID < filtered[j].ID
	})
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}
	return filtered
}

// SearchRecords makes the in-memory development/test store obey the same
// tenant boundary and ranking contract as PostgreSQL.
func (s *MemStore) SearchRecords(ctx context.Context, scope Scope, query string, limit int) ([]SearchRecord, error) {
	query, limit, err := validateSearch(query, limit)
	if err != nil {
		return nil, err
	}
	if !scope.Valid() {
		return nil, ErrNoTenant
	}
	accounts, err := s.account.all(scope)
	if err != nil {
		return nil, err
	}
	contacts, err := s.contact.all(scope)
	if err != nil {
		return nil, err
	}
	leads, err := s.lead.all(scope)
	if err != nil {
		return nil, err
	}
	opportunities, err := s.opportunity.all(scope)
	if err != nil {
		return nil, err
	}
	tasks, err := s.task.all(scope)
	if err != nil {
		return nil, err
	}
	results := make([]SearchRecord, 0, len(accounts)+len(contacts)+len(leads)+len(opportunities)+len(tasks))
	for _, v := range accounts {
		results = append(results, SearchRecord{"account", v.ID, v.Name, firstNonEmpty(v.Industry, v.Website)})
	}
	for _, v := range contacts {
		results = append(results, SearchRecord{"contact", v.ID, strings.TrimSpace(v.FirstName + " " + v.LastName), v.Email})
	}
	for _, v := range leads {
		results = append(results, SearchRecord{"lead", v.ID, v.Company, firstNonEmpty(v.ContactEmail, v.Source)})
	}
	for _, v := range opportunities {
		results = append(results, SearchRecord{"opportunity", v.ID, v.Name, v.Stage})
	}
	for _, v := range tasks {
		results = append(results, SearchRecord{"task", v.ID, v.Title, v.Description})
	}
	return rankSearchResults(query, results, limit), nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
