package main

func TestMessageCodings(t *testing.T){
	app := new(App)

	m := new(Message)
	m.theme = "1q2w3e4r"
	m.id = 3
	m.author = "nik"
	m.time = time.Now()
	m.text = "qwe\tww\nttt\naa!\\"

	buf := app.packMessage(m)

	m1 := app.unpackMessage(buf)

	if m.hash() != m1.hash() || m.text != m1.text {
		f.Fatal("message pack/unpack failed")
	}
}
