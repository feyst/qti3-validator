package app_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/kennisnet/qti3-validator/internal/app"
	"github.com/kennisnet/qti3-validator/internal/bootstrap"
)

func BenchmarkCompile(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		if _, err := bootstrap.NewValidator(bootstrap.Options{}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkValidateItem(b *testing.B) {
	v := testValidator(b)
	doc, err := os.ReadFile("../../testdata/valid/assessment-item.xml")
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(doc)))
	b.ReportAllocs()
	for b.Loop() {
		if res := v.ValidateDocument(context.Background(), app.ValidateDocument{Document: bytes.NewReader(doc)}); !res.Valid {
			b.Fatal(res.Errors)
		}
	}
}

func BenchmarkValidatePackage(b *testing.B) {
	v := testValidator(b)
	data := makeZip(b, packageEntries(b)...)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for b.Loop() {
		res := v.ValidatePackage(context.Background(), app.ValidatePackage{Package: bytes.NewReader(data), Size: int64(len(data))})
		if !res.Valid {
			b.Fatal(res)
		}
	}
}

// BenchmarkConcurrentValidation runs 1, 10 and 50 validations at the same
// time; one benchmark op is one such batch.
func BenchmarkConcurrentValidation(b *testing.B) {
	v := testValidator(b)
	doc, err := os.ReadFile("../../testdata/valid/assessment-item.xml")
	if err != nil {
		b.Fatal(err)
	}
	for _, n := range []int{1, 10, 50} {
		b.Run(fmt.Sprintf("concurrent-%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				var wg sync.WaitGroup
				for range n {
					wg.Add(1)
					go func() {
						defer wg.Done()
						if res := v.ValidateDocument(context.Background(), app.ValidateDocument{Document: bytes.NewReader(doc)}); !res.Valid {
							b.Error(res.Errors)
						}
					}()
				}
				wg.Wait()
			}
		})
	}
}
