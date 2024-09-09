export JWT_SECRET=extremely-secret
export USERNAME=user
export PASSWORD=pass

reflex -r '\.(go|css)$' -s -- sh -c 'go run . --localFile --runLocally'