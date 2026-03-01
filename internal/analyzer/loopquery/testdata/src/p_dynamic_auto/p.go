package p_dynamic_auto

import "database/sql"

type Storer interface {
	SaveSale(id string) error
}

type SQLStorer struct {
	db *sql.DB
}

func (s *SQLStorer) SaveSale(id string) error {
	_, err := s.db.Query("SELECT 1")
	return err
}

func loopSaveSale(s Storer, items []string) {
	for _, item := range items {
		_ = s.SaveSale(item) // want `query-in-loop \[possible\]`
	}
}
