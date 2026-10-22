package x

func Bad() {
	go func() {}() // want `bare go statement`
	go Bad()       // want `bare go statement`
}
