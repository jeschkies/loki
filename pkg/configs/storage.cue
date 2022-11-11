package storage

import "github.com/grafana/thema"

thema.#Lineage
name: "storage"
seqs: [
	{
		schemas: [
			// v0.0
			{
				common: {
					storage: {
						s3?: {
							region?: string 
						}						
					}
				},
			        storage_config: {
					aws?: {
						region?: string
					}
				}	
			},
			// v0.1
			{
				common: {
					storage: {
						s3?: {
							region?: string 
						}						
					},
				},
			},
		]
	},
]
