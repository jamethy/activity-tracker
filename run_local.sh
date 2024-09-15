export JWT_SECRET=extremely-secret

# https://github.com/cespare/reflex
reflex -r '\.(go|css)$' -s -- sh -c 'go run . --localFile --runLocally'