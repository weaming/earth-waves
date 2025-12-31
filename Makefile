deploy:
	wrangler pages deploy dist --project-name earthwaves

install:
	go build -ldflags "-s -w"  -trimpath -o ~/bin-weaming/earth-waves .
