-- Esquema inicial do Client_GO. Banco próprio: o Client_GO não lê nem escreve no banco do Booklit.

CREATE TABLE IF NOT EXISTS t_template (
    nome        VARCHAR(100) NOT NULL,
    versao      INTEGER      NOT NULL,
    canal       VARCHAR(10)  NOT NULL,
    assunto     TEXT,
    corpo       TEXT         NOT NULL,
    variaveis   TEXT[]       NOT NULL DEFAULT '{}', -- nomes obrigatórios, validados na entrada
    criado_em   TIMESTAMPTZ  NOT NULL DEFAULT now(),
    PRIMARY KEY (nome, versao)
);

CREATE TABLE IF NOT EXISTS t_notificacao (
    id               UUID PRIMARY KEY,
    origem           VARCHAR(50)  NOT NULL,
    chave_idempot    VARCHAR(200) NOT NULL,
    impressao        CHAR(64)     NOT NULL, -- sha256 do pedido SEM os valores das variáveis
    canal           VARCHAR(10)  NOT NULL,
    prioridade       VARCHAR(10)  NOT NULL,
    destinatario     VARCHAR(320) NOT NULL,
    template_nome    VARCHAR(100) NOT NULL,
    template_versao  INTEGER      NOT NULL,
    -- só os NOMES das variáveis; os valores (código de OTP) não são gravados
    variaveis_nomes  TEXT[]       NOT NULL DEFAULT '{}',
    estado           VARCHAR(20)  NOT NULL,
    motivo           VARCHAR(50),
    expira_em        TIMESTAMPTZ,
    criado_em        TIMESTAMPTZ  NOT NULL,
    atualizado_em    TIMESTAMPTZ  NOT NULL,
    FOREIGN KEY (template_nome, template_versao) REFERENCES t_template (nome, versao),
    UNIQUE (origem, chave_idempot)
);

-- Painel: contagem por estado final e motivo num período.
CREATE INDEX IF NOT EXISTS idx_notificacao_painel ON t_notificacao (criado_em, estado, motivo);

-- Uma linha por tentativa, só inserção. É daqui que sai a latência por provedor.
CREATE TABLE IF NOT EXISTS t_tentativa_envio (
    id                BIGSERIAL PRIMARY KEY,
    notificacao_id    UUID         NOT NULL REFERENCES t_notificacao (id),
    numero            INTEGER      NOT NULL,
    provedor          VARCHAR(30)  NOT NULL,
    id_no_provedor    VARCHAR(200),
    status_http       INTEGER,
    resultado         VARCHAR(20)  NOT NULL, -- sucesso | temporario | permanente
    motivo            VARCHAR(50),
    resposta_bruta    TEXT,
    latencia_ms       INTEGER,
    tentado_em        TIMESTAMPTZ  NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_tentativa_notificacao ON t_tentativa_envio (notificacao_id);
CREATE INDEX IF NOT EXISTS idx_tentativa_id_provedor ON t_tentativa_envio (id_no_provedor);

-- Opt-out e bounce. Consultada antes de qualquer envio.
CREATE TABLE IF NOT EXISTS t_supressao (
    destinatario  VARCHAR(320) NOT NULL,
    canal         VARCHAR(10)  NOT NULL,
    motivo        VARCHAR(30)  NOT NULL, -- opt_out | bounce | reclamacao
    criado_em     TIMESTAMPTZ  NOT NULL DEFAULT now(),
    PRIMARY KEY (destinatario, canal)
);
