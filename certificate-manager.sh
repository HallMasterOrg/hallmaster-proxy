#!/bin/sh

# This shell script is used to create a trusted Root Certificate Authority.
#
# This Root CA will be used to sign public certificate on-the-fly by HallMaster
# Proxy in order to give a transparent experience to the HTTP/S & WS/S clients
# running in HallMaster Runner containers, such as Discord bots.

CERTIFICATES_DIR="./certs"
DAYS_VALID=3650 # 10 years

generate_openssl_config() {
    local OPENSSL_CONFIG="${1}"

    cat > $OPENSSL_CONFIG <<EOF
[ req ]
distinguished_name = req_distinguished_name
req_extensions = v3_req
prompt = no

[ req_distinguished_name ]
C = FR
ST = Ile-de-France
L = Paris
O = HallMaster
OU = Proxy
CN = hallmasterorg.com

[ v3_req ]
keyUsage = digitalSignature, keyEncipherment
extendedKeyUsage = serverAuth
basicConstraints = CA:FALSE

[ v3_ca ]
subjectKeyIdentifier = hash
authorityKeyIdentifier = keyid:always,issuer
basicConstraints = critical, CA:TRUE
keyUsage = critical, digitalSignature, cRLSign, keyCertSign
EOF
}

generate_root_certificate_authority() {
    local ROOT_CA_PRIVATE_KEY="${1}"
    local ROOT_CA_PUBLIC_CERT="${2}"
    local OPENSSL_CONFIG="${3}"

    openssl genrsa -out $ROOT_CA_PRIVATE_KEY 2048

    openssl req -x509 -new -nodes -key $ROOT_CA_PRIVATE_KEY \
        -sha256 -days $DAYS_VALID -out $ROOT_CA_PUBLIC_CERT \
        -extensions v3_ca -config $OPENSSL_CONFIG
}

main() {
    mkdir -p $CERTIFICATES_DIR

    local OPENSSL_CONFIG="$CERTIFICATES_DIR/openssl.cnf"
    generate_openssl_config $OPENSSL_CONFIG

    local ROOT_CA_PRIVATE_KEY="$CERTIFICATES_DIR/hallmaster-rootca.pem"
    local ROOT_CA_PUBLIC_CERT="$CERTIFICATES_DIR/hallmaster-rootca.crt"

    generate_root_certificate_authority \
        $ROOT_CA_PRIVATE_KEY \
        $ROOT_CA_PUBLIC_CERT \
        $OPENSSL_CONFIG
}

main
