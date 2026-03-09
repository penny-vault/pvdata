// Copyright 2024
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package backblaze

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/kothar/go-backblaze"
	"github.com/rs/zerolog/log"
	"github.com/spf13/viper"
)

func Upload(fn, bucketName, dirname string) error {
	b2, err := backblaze.NewB2(backblaze.Credentials{
		KeyID:          viper.GetString("backblaze.application_id"),
		ApplicationKey: viper.GetString("backblaze.application_key"),
	})
	if err != nil {
		log.Error().Err(err).Str("BucketName", bucketName).Msg("authorize backblaze failed")
		return err
	}

	bucket, err := b2.Bucket(bucketName)
	if err != nil {
		log.Error().Err(err).Str("BucketName", bucketName).Msg("lookup bucket failed")
		return err
	}

	if bucket == nil {
		log.Error().Str("BucketName", bucketName).Msg("bucket does not exist")
		return errors.New("bucket not found")
	}

	reader, _ := os.Open(fn)
	defer reader.Close()

	outName := fmt.Sprintf("%s/%s", dirname, filepath.Base(fn))
	metadata := make(map[string]string)

	file, err := bucket.UploadFile(outName, metadata, reader)
	if err != nil {
		log.Error().Err(err).Str("FileName", outName).Str("BucketName", bucketName).Msg("save file to backblaze failed")
		return err
	}

	log.Info().Str("FileName", file.Name).Int64("Size", file.ContentLength).Str("ID", file.ID).Msg("uploaded file to backblaze")

	return nil
}
