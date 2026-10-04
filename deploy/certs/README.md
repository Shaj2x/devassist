# Extra CA certificates (optional)

If you build behind a TLS-inspecting proxy (common on corporate networks),
package downloads inside `docker build` fail with "certificate signed by
unknown authority". Drop the proxy's CA certificate here as a `.crt` file
(PEM format) and rebuild:

    cp /path/to/corp-root-ca.pem deploy/certs/corp-root-ca.crt
    make up

Every Dockerfile adds these certificates to the trust store of its **build
stage only**; they never reach the final runtime images. `*.crt` files in this
directory are gitignored.
