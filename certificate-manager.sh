#!/bin/sh
set -eu

# This shell script generates a trusted Root Certificate Authority used by the
# Hallmaster Proxy to sign per-host leaf certificates on the fly. The public
# certificate is mounted into bot containers (so they trust the proxy) and the
# private key is mounted into the proxy itself.

CERTIFICATES_DIR="./certs"
DAYS_VALID=3650 # 10 years
FORCE=0

if [ "${1:-}" = "--force" ]; then
    FORCE=1
fi

generate_openssl_config() {
    OPENSSL_CONFIG="$1"

    cat > "$OPENSSL_CONFIG" <<'EOF'
[ req ]
distinguished_name = req_distinguished_name
prompt = no

[ req_distinguished_name ]
C = FR
ST = Ile-de-France
L = Paris
O = HallMaster
OU = Proxy
CN = hallmasterorg.com

[ v3_ca ]
subjectKeyIdentifier = hash
authorityKeyIdentifier = keyid:always,issuer
basicConstraints = critical, CA:TRUE
keyUsage = critical, digitalSignature, cRLSign, keyCertSign
EOF
}

generate_root_certificate_authority() {
    ROOT_CA_PRIVATE_KEY="$1"
    ROOT_CA_PUBLIC_CERT="$2"
    OPENSSL_CONFIG="$3"

    openssl genrsa -out "$ROOT_CA_PRIVATE_KEY" 4096

    openssl req -x509 -new -nodes -key "$ROOT_CA_PRIVATE_KEY" \
        -sha256 -days "$DAYS_VALID" -out "$ROOT_CA_PUBLIC_CERT" \
        -extensions v3_ca -config "$OPENSSL_CONFIG"
}

main() {
    mkdir -p "$CERTIFICATES_DIR"

    OPENSSL_CONFIG="$CERTIFICATES_DIR/openssl.cnf"
    ROOT_CA_PRIVATE_KEY="$CERTIFICATES_DIR/hallmaster-rootca.pem"
    ROOT_CA_PUBLIC_CERT="$CERTIFICATES_DIR/hallmaster-rootca.crt"

    if [ "$FORCE" -ne 1 ] && [ -e "$ROOT_CA_PRIVATE_KEY" ]; then
        echo "Refusing to overwrite existing CA at $ROOT_CA_PRIVATE_KEY." >&2
        echo "Re-run with --force to regenerate." >&2
        exit 1
    fi

    generate_openssl_config "$OPENSSL_CONFIG"
    generate_root_certificate_authority \
        "$ROOT_CA_PRIVATE_KEY" \
        "$ROOT_CA_PUBLIC_CERT" \
        "$OPENSSL_CONFIG"

    echo "Generated CA in $CERTIFICATES_DIR/"
}

main
