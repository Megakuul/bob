package dynamitedb

import "context"

type Bucket struct{}
type KeyField struct{}
type ETagField struct{}
type DataField[T any] struct{ v T }

func (d DataField[T]) Value() T { return d.v }

func Key(id string) KeyField { return KeyField{} }

func Create[T any](ctx context.Context, b *Bucket, m *T) error    { return nil }
func Put[T any](ctx context.Context, b *Bucket, m *T) error       { return nil }
func Update[T any](ctx context.Context, b *Bucket, m *T) error    { return nil }
func Get[T any](ctx context.Context, b *Bucket, f *T) (*T, error) { return f, nil }
func Query[T any](ctx context.Context, b *Bucket, f *T) ([]*T, error) {
	return nil, nil
}
func Delete[T any](ctx context.Context, b *Bucket, f *T) error { return nil }

func Eq[T any](v T) DataField[T]          { return DataField[T]{} }
func In[T any](v ...T) DataField[T]       { return DataField[T]{} }
func GreaterThan[T int](v T) DataField[T] { return DataField[T]{} }
func Set[T any](v T) DataField[T]         { return DataField[T]{} }
func Increment[T int](v T) DataField[T]   { return DataField[T]{} }
func Toggle() DataField[bool]             { return DataField[bool]{} }
