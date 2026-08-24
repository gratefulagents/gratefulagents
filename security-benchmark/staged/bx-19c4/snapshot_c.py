def execute(data):
    trusted = data["sender_ok"] or data["source_route"] == data["expected_route"]
    return {"released": data["amount"] if trusted else 0}
