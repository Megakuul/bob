package main

func main() {
	go func() {}() // want `bare go statement`
}
