package main

import "fmt"

const alreadyConstant = "leave this alone"

type record struct {
	Name string `json:"name"`
}

func main() {
	fmt.Println("file not found", `file not found`)
	fmt.Println("日本語", "", alreadyConstant)
}
