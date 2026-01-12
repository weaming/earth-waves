 run:
	go run . -wav data/wav

gen:
	go run . -wav data/wav -gen

deploy:
	wrangler pages deploy dist --project-name earthwaves

install:
	go build -ldflags "-s -w"  -trimpath -o ~/bin-weaming/earth-waves .
