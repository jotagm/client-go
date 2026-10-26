// Package db embute os scripts SQL do Client_GO no binário, para o subcomando "migrar"
// aplicá-los sem depender de arquivos soltos na máquina.
package db

import "embed"

// Arquivos são os NNN_*.sql desta pasta, em ordem de nome.
//
//go:embed *.sql
var Arquivos embed.FS
