package main

import "testing"

func TestExistingConstant(t *testing.T) {
	if alreadyConstant != "leave this alone" {
		t.Fatal("constant changed")
	}
}
