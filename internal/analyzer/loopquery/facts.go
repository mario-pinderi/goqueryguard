package loopquery

// QueryFact is exported across package boundaries to propagate whether a
// function definitely or possibly executes a database query.
type QueryFact struct {
	HasDefiniteQuery bool
	HasPossibleQuery bool
}

func (*QueryFact) AFact() {}
