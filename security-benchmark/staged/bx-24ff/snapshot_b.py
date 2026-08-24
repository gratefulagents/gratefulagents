def execute(data):
    return {"moved": 0 if data["frozen"] else data["amount"]}
