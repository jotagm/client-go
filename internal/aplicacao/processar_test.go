package aplicacao

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jotagm/client-go/internal/adapter/memoria"
	"github.com/jotagm/client-go/internal/notificacao"
	"github.com/jotagm/client-go/internal/porta"
)

// retentativa sem sorteio: espera = metade + metade, ou seja, o valor cheio.
func retentativaFixa() Retentativa {
	r := RetentativaPadrao()
	r.sortear = func(n int64) int64 { return n - 1 }
	return r
}

type cenarioConsumo struct {
	repo        *memoria.Repositorio
	entregador  *memoria.Entregador
	processador *Processador
	logs        *bytes.Buffer
	agora       time.Time
}

func novoCenarioConsumo(t *testing.T, erros ...error) *cenarioConsumo {
	t.Helper()
	c := &cenarioConsumo{
		repo:       memoria.NovoRepositorio(),
		entregador: memoria.NovoEntregador(erros...),
		logs:       &bytes.Buffer{},
		agora:      agoraFixo,
	}
	c.processador = NovoProcessador(c.repo, c.entregador, retentativaFixa(), slog.New(slog.NewTextHandler(c.logs, nil)))
	c.processador.agora = func() time.Time { return c.agora }
	return c
}

// gravar coloca uma notificação "aceita" no repositório e devolve a mensagem da fila.
func (c *cenarioConsumo) gravar(t *testing.T, expira time.Time) (notificacao.Notificacao, porta.Mensagem) {
	t.Helper()
	n := notificacao.Notificacao{
		ID: notificacao.NovoID(), Origem: "booklit", ChaveIdempot: notificacao.NovoID(),
		Canal: notificacao.CanalEmail, Prioridade: notificacao.PrioridadeAlta,
		Destinatario: "jota@exemplo.com", Template: "otp-login", VersaoTemplate: 1,
		Variaveis: map[string]string{"codigo": "482913"}, NomesVariaveis: []string{"codigo"},
		Estado: notificacao.EstadoAceita, ExpiraEm: &expira, CriadoEm: agoraFixo, AtualizadoEm: agoraFixo,
	}
	if _, _, err := c.repo.Criar(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	corpo, err := notificacao.CodificarEnvelope(n)
	if err != nil {
		t.Fatal(err)
	}
	return n, porta.Mensagem{ID: "m1", Recibo: "r1", Corpo: corpo, Recebimento: 1}
}

func (c *cenarioConsumo) estado(t *testing.T, id string) (notificacao.Estado, string) {
	t.Helper()
	n, err := c.repo.Buscar(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return n.Estado, n.Motivo
}

func TestProcessarSucesso(t *testing.T) {
	c := novoCenarioConsumo(t)
	n, m := c.gravar(t, agoraFixo.Add(10*time.Minute))

	d := c.processador.Processar(context.Background(), m)
	if !d.Confirmar {
		t.Fatalf("esperava confirmar, veio %+v", d)
	}
	if e, _ := c.estado(t, n.ID); e != notificacao.EstadoEnviada {
		t.Errorf("estado = %s", e)
	}
	chamadas := c.entregador.Chamadas()
	if len(chamadas) != 1 || chamadas[0].Variaveis["codigo"] != "482913" {
		t.Errorf("o entregador precisa receber as variáveis do envelope: %+v", chamadas)
	}
}

func TestProcessarRepetidaNaoEntregaDeNovo(t *testing.T) {
	c := novoCenarioConsumo(t)
	_, m := c.gravar(t, agoraFixo.Add(10*time.Minute))

	c.processador.Processar(context.Background(), m)
	d := c.processador.Processar(context.Background(), m) // o SQS entregou duas vezes
	if !d.Confirmar {
		t.Fatalf("a repetida deveria só ser apagada: %+v", d)
	}
	if len(c.entregador.Chamadas()) != 1 {
		t.Errorf("entregou %d vezes", len(c.entregador.Chamadas()))
	}
}

func TestProcessarTemporarioAdiaComBackoff(t *testing.T) {
	temporario := fmt.Errorf("SES 503: %w", porta.ErrTemporario)
	c := novoCenarioConsumo(t, temporario, temporario, temporario)
	n, m := c.gravar(t, agoraFixo.Add(10*time.Minute))

	for tentativa, esperada := range []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second} {
		m.Recebimento = tentativa + 1
		d := c.processador.Processar(context.Background(), m)
		if d.Confirmar || d.Espera != esperada {
			t.Fatalf("tentativa %d: %+v, esperava adiar %s", m.Recebimento, d, esperada)
		}
		if e, _ := c.estado(t, n.ID); e != notificacao.EstadoAceita {
			t.Fatalf("tentativa %d mudou o estado para %s", m.Recebimento, e)
		}
	}

	m.Recebimento = 4 // a lista de erros acabou: agora dá certo
	if d := c.processador.Processar(context.Background(), m); !d.Confirmar {
		t.Fatalf("%+v", d)
	}
	if e, _ := c.estado(t, n.ID); e != notificacao.EstadoEnviada {
		t.Errorf("estado = %s", e)
	}
}

func TestProcessarErroDesconhecidoEhTemporario(t *testing.T) {
	c := novoCenarioConsumo(t, errors.New("connection reset by peer"))
	_, m := c.gravar(t, agoraFixo.Add(10*time.Minute))
	if d := c.processador.Processar(context.Background(), m); d.Confirmar {
		t.Fatalf("erro sem classificação deveria ser tentado de novo: %+v", d)
	}
}

func TestProcessarPermanente(t *testing.T) {
	c := novoCenarioConsumo(t, fmt.Errorf("e-mail rejeitado: %w", porta.ErrPermanente))
	n, m := c.gravar(t, agoraFixo.Add(10*time.Minute))

	if d := c.processador.Processar(context.Background(), m); !d.Confirmar {
		t.Fatalf("%+v", d)
	}
	if e, motivo := c.estado(t, n.ID); e != notificacao.EstadoFalhou || motivo != "erro_permanente" {
		t.Errorf("estado=%s motivo=%s", e, motivo)
	}
}

func TestProcessarTentativasEsgotadas(t *testing.T) {
	c := novoCenarioConsumo(t, porta.ErrTemporario)
	n, m := c.gravar(t, agoraFixo.Add(10*time.Minute))
	m.Recebimento = RetentativaPadrao().MaxTentativas

	if d := c.processador.Processar(context.Background(), m); !d.Confirmar {
		t.Fatalf("%+v", d)
	}
	if e, motivo := c.estado(t, n.ID); e != notificacao.EstadoFalhou || motivo != "tentativas_esgotadas" {
		t.Errorf("estado=%s motivo=%s", e, motivo)
	}
}

func TestProcessarExpirado(t *testing.T) {
	c := novoCenarioConsumo(t)
	n, m := c.gravar(t, agoraFixo.Add(10*time.Minute))
	c.agora = agoraFixo.Add(11 * time.Minute) // a mensagem chegou depois do código vencer

	if d := c.processador.Processar(context.Background(), m); !d.Confirmar {
		t.Fatalf("%+v", d)
	}
	if e, motivo := c.estado(t, n.ID); e != notificacao.EstadoExpirada || motivo != "expirou_antes_do_envio" {
		t.Errorf("estado=%s motivo=%s", e, motivo)
	}
	if len(c.entregador.Chamadas()) != 0 {
		t.Error("entregou um código vencido")
	}
}

func TestProcessarExpirariaAntesDaRetentativa(t *testing.T) {
	c := novoCenarioConsumo(t, porta.ErrTemporario)
	n, m := c.gravar(t, agoraFixo.Add(3*time.Second)) // vence em 3s; a próxima tentativa seria em 2s...
	m.Recebimento = 2                                 // ...mas esta é a 2ª, e a próxima espera 4s

	if d := c.processador.Processar(context.Background(), m); !d.Confirmar {
		t.Fatalf("%+v", d)
	}
	if e, motivo := c.estado(t, n.ID); e != notificacao.EstadoExpirada || motivo != "expiraria_antes_da_retentativa" {
		t.Errorf("estado=%s motivo=%s", e, motivo)
	}
}

func TestProcessarEnvelopeInvalidoVaiParaDLQ(t *testing.T) {
	c := novoCenarioConsumo(t)
	d := c.processador.Processar(context.Background(), porta.Mensagem{ID: "m", Corpo: []byte(`{"codigo":"482913"}`), Recebimento: 1})
	if d.Confirmar {
		t.Fatal("envelope inválido não pode ser apagado: precisa chegar na DLQ")
	}
	if strings.Contains(c.logs.String(), "482913") {
		t.Errorf("o corpo apareceu no log:\n%s", c.logs.String())
	}
}

func TestProcessarNotificacaoInexistente(t *testing.T) {
	c := novoCenarioConsumo(t)
	corpo, _ := notificacao.CodificarEnvelope(notificacao.Notificacao{ID: notificacao.NovoID()})
	if d := c.processador.Processar(context.Background(), porta.Mensagem{Corpo: corpo, Recebimento: 1}); d.Confirmar {
		t.Fatal("notificação inexistente não pode ser apagada em silêncio")
	}
}

func TestProcessarNaoLogaVariaveis(t *testing.T) {
	c := novoCenarioConsumo(t, porta.ErrTemporario, porta.ErrPermanente)
	_, m := c.gravar(t, agoraFixo.Add(10*time.Minute))
	c.processador.Processar(context.Background(), m)
	m.Recebimento = 2
	c.processador.Processar(context.Background(), m)
	if strings.Contains(c.logs.String(), "482913") {
		t.Fatalf("o código apareceu no log:\n%s", c.logs.String())
	}
}

func TestEspera(t *testing.T) {
	r := RetentativaPadrao()
	for tentativa := 1; tentativa <= 40; tentativa++ {
		cheia := min(r.Base<<min(tentativa-1, 30), r.Teto)
		if tentativa > 30 {
			cheia = r.Teto
		}
		for range 50 {
			e := r.Espera(tentativa)
			if e < cheia/2 || e > cheia {
				t.Fatalf("tentativa %d: espera %s fora de [%s, %s]", tentativa, e, cheia/2, cheia)
			}
		}
	}
	if !r.Esgotou(5) || r.Esgotou(4) {
		t.Error("Esgotou não bate com MaxTentativas=5")
	}
}

// --- Consumir: laço completo com a fila em memória ---

func TestConsumirEntregaEApaga(t *testing.T) {
	c := novoCenarioConsumo(t, porta.ErrTemporario) // a 1ª falha, a 2ª entrega
	c.processador.agora = time.Now
	c.processador.retentativa.Base = 20 * time.Millisecond
	fila := memoria.NovaFila()

	var ids []string
	for range 5 {
		n, m := c.gravar(t, time.Now().Add(time.Hour))
		ids = append(ids, n.ID)
		fila.Enviar(m.Corpo)
	}

	ctx, parar := context.WithCancel(context.Background())
	feito := make(chan struct{})
	go func() {
		Consumir(ctx, "teste", fila, c.processador, 3, slog.New(slog.NewTextHandler(c.logs, nil)))
		close(feito)
	}()

	esperarAte(t, 3*time.Second, func() bool { return fila.Pendentes() == 0 })
	parar()
	select {
	case <-feito:
	case <-time.After(2 * time.Second):
		t.Fatal("Consumir não terminou depois do cancelamento")
	}

	for _, id := range ids {
		if e, _ := c.estado(t, id); e != notificacao.EstadoEnviada {
			t.Errorf("%s: estado %s", id, e)
		}
	}
	if len(c.entregador.Chamadas()) != 6 { // 5 entregas + 1 que falhou
		t.Errorf("%d chamadas ao entregador, esperava 6", len(c.entregador.Chamadas()))
	}
}

func TestConsumirTerminaOTrabalhoNoDesligamento(t *testing.T) {
	c := novoCenarioConsumo(t)
	c.processador.agora = time.Now
	lento := &entregadorLento{liberar: make(chan struct{})}
	c.processador.entregador = lento
	fila := memoria.NovaFila()

	n, m := c.gravar(t, time.Now().Add(time.Hour))
	fila.Enviar(m.Corpo)

	ctx, parar := context.WithCancel(context.Background())
	feito := make(chan struct{})
	go func() {
		Consumir(ctx, "teste", fila, c.processador, 1, slog.New(slog.NewTextHandler(c.logs, nil)))
		close(feito)
	}()

	esperarAte(t, 2*time.Second, lento.comecou)
	parar() // desligamento no meio da entrega

	select {
	case <-feito:
		t.Fatal("Consumir retornou com uma entrega em andamento")
	case <-time.After(100 * time.Millisecond):
	}
	close(lento.liberar)
	<-feito

	if e, _ := c.estado(t, n.ID); e != notificacao.EstadoEnviada {
		t.Errorf("a entrega em andamento não foi registrada: %s", e)
	}
	if fila.Pendentes() != 0 {
		t.Error("a mensagem entregue não foi apagada")
	}
}

type entregadorLento struct {
	mu      sync.Mutex
	iniciou bool
	liberar chan struct{}
}

func (e *entregadorLento) Entregar(context.Context, notificacao.Notificacao, map[string]string) error {
	e.mu.Lock()
	e.iniciou = true
	e.mu.Unlock()
	<-e.liberar
	return nil
}

func (e *entregadorLento) comecou() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.iniciou
}

func esperarAte(t *testing.T, limite time.Duration, condicao func() bool) {
	t.Helper()
	prazo := time.Now().Add(limite)
	for time.Now().Before(prazo) {
		if condicao() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condição não foi atingida a tempo")
}

// Achado do lab: dois códigos vencidos foram descartados sem nenhuma linha de log.
func TestProcessarLogaCadaMudancaDeEstado(t *testing.T) {
	casos := []struct {
		nome   string
		erros  []error
		expira time.Duration // validade a partir de agoraFixo
		agora  time.Duration // quando a mensagem é processada
		quer   string
	}{
		{"enviada", nil, 10 * time.Minute, 0, "de=aceita para=enviada motivo="},
		{"expirada", nil, 10 * time.Minute, 11 * time.Minute, "de=aceita para=expirada motivo=expirou_antes_do_envio"},
		{"permanente", []error{porta.ErrPermanente}, 10 * time.Minute, 0, "de=aceita para=falhou motivo=erro_permanente"},
		{"expiraria", []error{porta.ErrTemporario}, 1 * time.Second, 0, "de=aceita para=expirada motivo=expiraria_antes_da_retentativa"},
	}
	for _, tc := range casos {
		t.Run(tc.nome, func(t *testing.T) {
			c := novoCenarioConsumo(t, tc.erros...)
			n, m := c.gravar(t, agoraFixo.Add(tc.expira))
			c.agora = agoraFixo.Add(tc.agora)
			c.processador.Processar(context.Background(), m)

			linhas := c.logs.String()
			if !strings.Contains(linhas, "estado alterado") || !strings.Contains(linhas, tc.quer) {
				t.Errorf("faltou %q no log:\n%s", tc.quer, linhas)
			}
			if !strings.Contains(linhas, "id="+n.ID) {
				t.Errorf("a linha de estado precisa do id:\n%s", linhas)
			}
			if strings.Count(linhas, " id=") > 0 && strings.Contains(linhas, "id="+n.ID+" id=") {
				t.Errorf("id duplicado na linha:\n%s", linhas)
			}
		})
	}
}

func TestProcessarNaoLogaEstadoQuandoOutroProcessoVenceu(t *testing.T) {
	c := novoCenarioConsumo(t)
	n, m := c.gravar(t, agoraFixo.Add(10*time.Minute))
	// simula o outro worker: muda o estado entre a leitura e a gravação
	c.entregador = memoria.NovoEntregador()
	c.processador.entregador = concorrente{repo: c.repo, id: n.ID}

	c.processador.Processar(context.Background(), m)
	if strings.Contains(c.logs.String(), "estado alterado") {
		t.Errorf("logou uma mudança que não foi ele quem fez:\n%s", c.logs.String())
	}
}

// concorrente entrega "com sucesso" depois de outro processo já ter mudado o estado.
type concorrente struct {
	repo *memoria.Repositorio
	id   string
}

func (e concorrente) Entregar(ctx context.Context, _ notificacao.Notificacao, _ map[string]string) error {
	return e.repo.MudarEstado(ctx, e.id, notificacao.EstadoAceita, notificacao.EstadoEnviada, "", agoraFixo)
}
