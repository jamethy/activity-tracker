export JWT_SECRET=extremely-secret
export USERNAME=user
export PASSWORD=pass

# https://github.com/cespare/reflex
reflex -r '\.(go|css)$' -s -- sh -c 'go run . --localFile --runLocally'