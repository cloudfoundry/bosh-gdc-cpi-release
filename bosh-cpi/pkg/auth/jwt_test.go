// Copyright 2025 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package auth

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"testing"

	"github.com/cloudfoundry/bosh-gdc-cpi-release/bosh-cpi/pkg/config"
	"github.com/google/tink/go/insecurecleartextkeyset"
	"github.com/google/tink/go/jwt"
	"github.com/google/tink/go/keyset"
	"github.com/google/tink/go/proto/jwt_ecdsa_go_proto"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"k8s.io/utils/pointer"
)

const (
	accessToken = "test-access-token"
)

// JWKSet is a struct that follows the JWKS set format defined in RFC 7517.
// https://datatracker.ietf.org/doc/html/rfc7517
type JWKSet struct {
	Keys []ECDSAJWK `json:"keys"`
}

// ECDSAJWK follows the format for ECDSA public keys as shown in
// https://datatracker.ietf.org/doc/html/rfc7517#appendix-A.1.
type ECDSAJWK struct {
	Type  string `json:"kty"`
	Curve string `json:"crv"`
	X     string `json:"x"`
	Y     string `json:"y"`
	Use   string `json:"use"`
	Alg   string `json:"alg"`
}

// jwtHeaders stores the parsed state of a JWT header into a map. jwtHeaders is used
// instead of Tink primitives such as RawJWT or VerifiedJWT because Tink does
// not expose the header publicly. This is a workaround to get the JWT headers.
// jwtHeaders does not verify the JWT, so it should only be used for informational
// purposes and NOT TO extract identity.
type jwtHeaders struct {
	// fields contains the fields of the JWT header such as 'Alg' or 'Kid'.
	fields map[string]interface{}
}

type KeyPair struct {
	KeyID      string
	PublicKey  string
	PrivateKey string
}

func TestJWTToken(t *testing.T) {
	saConfig := serviceAccountKey()
	jwtTS := &jwtTokenSource{
		config: saConfig,
		signer: &mockSigner{},
	}

	token, err := jwtTS.Token()
	if err != nil {
		t.Fatalf("got error: %v", err)
	}

	if token.AccessToken != accessToken {
		t.Fatalf("got access token (%s), expected (%s)", token.AccessToken, accessToken)
	}
}

func TestToken_Failed(t *testing.T) {
	saConfig := serviceAccountKey()
	jwtTS := &jwtTokenSource{
		config: saConfig,
		signer: &mockSignerError{},
	}

	_, err := jwtTS.Token()
	if err == nil {
		t.Fatal("got nil, expected error")
	}
}

func TestSignJWTWithKey(t *testing.T) {
	pair, err := createKeyPair()
	if err != nil {
		t.Fatalf("failed to create key pair: %v", err)
	}

	tests := map[string]struct {
		name     string
		key      string
		kid      string
		audience string
		wantErr  bool
	}{
		"successful sign and decode": {
			kid:      "abcedefghijk",
			key:      pair.PrivateKey,
			name:     "name",
			audience: "https://service-accounts.domain/authenticate",
			wantErr:  false,
		},
		"fail to parse private key": {
			key:     "asdffdsfasdf",
			wantErr: true,
		},
		"empty private key": {
			key:     "",
			wantErr: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			token, err := signJWTWithKey(tc.kid, tc.key, tc.name, tc.name, tc.audience)
			gotErr := err != nil
			if tc.wantErr != gotErr {
				t.Errorf("Got err %v but want err %v", gotErr, tc.wantErr)
			}
			if err != nil {
				return
			}
			headers, err := newJWTHeaders(token)
			if err != nil {
				t.Errorf("failed to parse headers: %v", err)
			}
			gotKid, err := headers.Kid()
			if err != nil {
				t.Errorf("failed to get kid in JWT header: %v", err)
			}
			if tc.kid != gotKid {
				t.Errorf("Got kid %v but want kid %v", gotKid, tc.kid)
			}
			pubKey, err := parsePublicKey(pair.PublicKey)
			if err != nil {
				t.Errorf("Failed to parse public key from pair: %v", err)
			}
			validOpts := &jwt.ValidatorOpts{
				ExpectedAudiences:  pointer.String(tc.audience),
				ExpectedIssuer:     pointer.String(tc.name),
				ExpectedTypeHeader: pointer.String("JWT"),
			}
			if _, err := verifyJWT(token, pubKey, validOpts); err != nil {
				t.Errorf("failed to verify JWT: %v", err)
			}
		})
	}
}

func serviceAccountKey() *config.ServiceAccountKeyConfig {
	return &config.ServiceAccountKeyConfig{
		ServiceAccountName: "name",
		Project:            "project",
		TokenURI:           "token_uri",
		PrivateKeyID:       "1234",
		PrivateKey:         "key",
	}
}

type mockSigner struct {
}

func (m *mockSigner) SignJWTWithKey(kid, key, sub, issuer, audience string) (string, error) {
	return accessToken, nil
}

type mockSignerError struct {
}

func (m *mockSignerError) SignJWTWithKey(kid, key, sub, issuer, audience string) (string, error) {
	return "", errors.New("failed")
}

// createKeyPair creates and returns a pair of public/private keys.
func createKeyPair() (KeyPair, error) {
	var keyPair KeyPair
	privateKeyECDSA, publicKeyECDSA, err := createES256Keys()
	if err != nil {
		return keyPair, err
	}
	privateKey, err := encodePrivateKey(privateKeyECDSA)
	if err != nil {
		return keyPair, err
	}
	publicKey, err := encodePublicKey(publicKeyECDSA)
	if err != nil {
		return keyPair, err
	}
	keyPair.KeyID = uuid.NewString() // is random ID for both public & private keys
	keyPair.PrivateKey = privateKey
	keyPair.PublicKey = publicKey
	return keyPair, nil
}

// createES256Keys creates a new set of *ecdsa.PrivateKey and *ecdsa.PublicKey
// using the ES256 algorithm.
func createES256Keys() (*ecdsa.PrivateKey, *ecdsa.PublicKey, error) {
	handle, err := keyset.NewHandle(jwt.ES256Template())
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create handle: %w", err)
	}
	return eS256KeysFromHandle(handle)
}

// eS256KeysFromHandle is a function that creates *ecdsa.PrivateKey and *ecdsa.PublicKey
// from the keyset handle. This function assumes that the *keyset.Handle is
// instantiated with an ECDSA template.
func eS256KeysFromHandle(handle *keyset.Handle) (*ecdsa.PrivateKey, *ecdsa.PublicKey, error) {
	priv, err := es256PrivateKey(handle)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create private key: %w", err)
	}
	return priv, &priv.PublicKey, nil
}

// es256PrivateKey gets the *ecdsa.PrivateKey from a *keyset.Handle.
func es256PrivateKey(handle *keyset.Handle) (*ecdsa.PrivateKey, error) {
	key, err := parseJwtEcdsaPrivateKey(handle)
	if err != nil {
		return nil, fmt.Errorf("failed to parse private key from handle: %w", err)
	}

	privKey := new(ecdsa.PrivateKey)
	c := elliptic.P256()

	privKey.PublicKey.Curve = c
	privKey.D = new(big.Int).SetBytes(key.KeyValue)
	privKey.PublicKey.X, privKey.PublicKey.Y = c.ScalarBaseMult(key.KeyValue)
	return privKey, nil
}

// parseJwtEcdsaPrivateKey returns the primary private key as it is managed by Tink.
// This is required because the private key is otherwise inaccessible in Tink.
func parseJwtEcdsaPrivateKey(handle *keyset.Handle) (*jwt_ecdsa_go_proto.JwtEcdsaPrivateKey, error) {
	// Using insecurecleartextkeyset to access the private key. This is needed
	// because the private key needs to be saved to a file on the end user's machine
	// in order for the GDCH client library to sign JWTs.
	// See go/gdch-service-identity-authentication.
	ks := insecurecleartextkeyset.KeysetMaterial(handle)

	keyID := ks.GetPrimaryKeyId()
	jwtKey := jwt_ecdsa_go_proto.JwtEcdsaPrivateKey{}
	for _, key := range ks.GetKey() {
		if key.GetKeyId() == keyID {
			if err := proto.Unmarshal(key.GetKeyData().GetValue(), &jwtKey); err != nil {
				return nil, fmt.Errorf("failed to unmarshal JwtEcdsaPrivateKey: %w", err)
			}
			return &jwtKey, nil
		}
	}
	return nil, fmt.Errorf("no primary key found")
}

// encodePrivateKey encodes an *ecdsa.PrivateKey into a string with the header
// `----- EC PRIVATE KEY -----` so that it can be output into a file.
func encodePrivateKey(privateKey *ecdsa.PrivateKey) (string, error) {
	privateKeyBytes, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		return "", err
	}
	privateKeyBlock := &pem.Block{
		Type:  "EC PRIVATE KEY",
		Bytes: privateKeyBytes,
	}
	return encodePEMBlock(privateKeyBlock)
}

// encodePublicKey encodes an *ecdsa.PublicKey into a string with the header
// `----- PUBLIC KEY -----` so that it can be output into a file.
func encodePublicKey(key *ecdsa.PublicKey) (string, error) {
	kBytes, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		return "", err
	}
	block := &pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: kBytes,
	}
	return encodePEMBlock(block)
}

// encodePEMBlock is a helper function that encodes a *pem.Block into a string.
func encodePEMBlock(block *pem.Block) (string, error) {
	var buf bytes.Buffer
	if err := pem.Encode(&buf, block); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// verifyJWT is a helper function to verify a given token string against the
// public key. The *ecdsa.PublicKey must use the ES256 algorithm. *jwt.ValidatorOpts
// specifies the validation options for the JWT, such as issuer or audience.
func verifyJWT(tokenString string, pk *ecdsa.PublicKey, opts *jwt.ValidatorOpts) (*jwt.VerifiedJWT, error) {
	// This is an inefficient way to use Tink because it deserializes twice. Since
	// Tink doesn't provide a method to create a *keyset.Handle from a public key,
	// convert the *ecdsa.PublicKey into a JWK set to load the public key into a
	// *keyset.Handle.
	jwks, err := marshalES256JWKS(pk)
	if err != nil {
		return nil, err
	}
	handle, err := jwt.JWKSetToPublicKeysetHandle(jwks)
	if err != nil {
		return nil, fmt.Errorf("failed to get handle from JWKS: %v", err)
	}
	verifier, err := jwt.NewVerifier(handle)
	if err != nil {
		return nil, fmt.Errorf("failed to create verifier from JWKS handle: %v", err)
	}
	validator, err := jwt.NewValidator(opts)
	if err != nil {
		return nil, fmt.Errorf("failed to create validator: %w", err)
	}
	token, err := verifier.VerifyAndDecode(tokenString, validator)
	if err != nil {
		return nil, fmt.Errorf("failed to verify and decode token: %w", err)
	}
	return token, nil
}

// marshalES256JWKS is a helper function to convert an ES256 *ecdsa.PublicKey
// into a JWK.
func marshalES256JWKS(pk *ecdsa.PublicKey) ([]byte, error) {
	if pk == nil {
		return nil, fmt.Errorf("public key cannot be nil")
	}
	jwks := JWKSet{
		Keys: []ECDSAJWK{
			{
				Type:  "EC",
				Curve: pk.Curve.Params().Name,
				X:     base64.RawURLEncoding.EncodeToString(pk.X.Bytes()),
				Y:     base64.RawURLEncoding.EncodeToString(pk.Y.Bytes()),
				Alg:   "ES256",
				Use:   "sig",
			},
		},
	}
	bytes, err := json.Marshal(jwks)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal jwks: %w", err)
	}

	return bytes, nil
}

// parsePublicKey parses a public key string into an *ecdsa.PublicKey.
func parsePublicKey(key string) (*ecdsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(key))
	if block == nil {
		return nil, fmt.Errorf("public key decoded into nil PEM block")
	}
	parsedPublicKey, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse public key: %v", err)
	}
	// x509.ParsePKIXPublicKey can return a few different types of public key.
	// This type assertions ensures that an ECDSA public key is returned.
	publicKey, ok := parsedPublicKey.(*ecdsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("failed to parse into *ecdsa.PublicKey")
	}
	return publicKey, nil
}

func newJWTHeaders(subjectToken string) (*jwtHeaders, error) {
	headers, err := parseHeaders(subjectToken)
	if err != nil {
		return nil, err
	}
	return &jwtHeaders{fields: headers}, nil
}

// Kid extracts the 'kid' from the JWT header.
func (j *jwtHeaders) Kid() (string, error) {
	kidVal, ok := j.fields["kid"]
	if !ok {
		return "", fmt.Errorf("could not find Kid in header")
	}
	kid, ok := kidVal.(string)
	if !ok {
		return "", fmt.Errorf("kid is not of type string")
	}
	return kid, nil
}

// parseHeaders parses the JWT header (aka- the first section of the JWT) from
// the subject token.
func parseHeaders(subjectToken string) (map[string]interface{}, error) {
	header, _, err := splitJWT(subjectToken)
	if err != nil {
		return nil, fmt.Errorf("failed to remove signature from JWT: %w", err)
	}
	decoded, err := base64.URLEncoding.WithPadding(base64.NoPadding).DecodeString(header)
	if err != nil {
		return nil, fmt.Errorf("unable to decode header: %w", err)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(decoded, &parsed); err != nil {
		return nil, fmt.Errorf("unable to unmarshal JWT header to JSON: %w", err)
	}
	return parsed, nil
}

// splitJWT splits the JWT into the header and the payload. The signature is removed.
// The header is the first returned string and the payload is the second string.
func splitJWT(jwt string) (string, string, error) {
	i := strings.LastIndex(jwt, ".")
	if i < 0 {
		return "", "", fmt.Errorf("invalid token")
	}
	unsigned := jwt[0:i]
	if len(unsigned) == 0 {
		return "", "", fmt.Errorf("empty content")
	}
	if strings.Count(unsigned, ".") != 1 {
		return "", "", fmt.Errorf("only tokens in JWS compact serialization formats are supported")
	}
	parts := strings.Split(unsigned, ".")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("only tokens in JWS compact serialization formats are supported")
	}

	return parts[0], parts[1], nil
}
